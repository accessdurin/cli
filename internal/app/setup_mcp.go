package app

import (
	"context"
	"net/http"
	"os"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
)

// mcpIssuer is the personal endpoint's one trusted authorization server, from its protected
// resource metadata (src/setup-mcp.ts). request must already keep requests on the
// deployment's hosts.
func mcpIssuer(ctx context.Context, resource string, request *http.Client, scope contracts.CliHostScope) (string, error) {
	if !contracts.SafeCliURL(resource, scope.Development) {
		return "", clierror.New("The personal MCP endpoint is unsafe.")
	}
	if err := assertExpectedHost(contracts.RoleResource, resource, scope); err != nil {
		return "", err
	}
	metadata, err := oauth.DiscoverOAuthProtectedResourceMetadata(ctx, resource, nil, HTTPFetch(request))
	if err != nil {
		return "", err
	}
	issuers := metadata.AuthorizationServers
	if metadata.Resource != resource || len(issuers) != 1 {
		return "", clierror.New("MCP discovery must identify the exact personal resource and one trusted issuer.")
	}
	issuer := issuers[0]
	if issuer == "" || !contracts.SafeCliURL(issuer, scope.Development) {
		return "", clierror.New("Unsafe MCP authorization issuer.")
	}
	if err := assertExpectedHost(contracts.RoleMcpIssuer, issuer, scope); err != nil {
		return "", err
	}
	return contracts.CanonicalIssuer(contracts.RoleMcpIssuer, issuer, scope)
}

func assertExpectedHost(role contracts.CliHostRole, url string, scope contracts.CliHostScope) error {
	problem, err := contracts.UntrustedCliHost(role, url, scope)
	if err != nil {
		return err
	}
	if problem != "" {
		return clierror.New(problem + " Check --app-url, or ask your durin administrator.")
	}
	return nil
}

// authorizeMcpOptions are what authorizeMcp needs.
type authorizeMcpOptions struct {
	Profile contracts.CliProfile
	Store   ports.CredentialStore
	Marker  ports.RevocationMarker
	Pending ports.PendingRevocations
	Request *http.Client
	Open    func(ctx context.Context, url string) error
}

// authorizeMcp signs the profile in to its MCP server and returns how many tools its
// authenticated catalog lists (src/setup-mcp.ts). It runs under the profile lock. A grant
// kept by an unfinished logout is retired, never reused. Every request, including those to
// endpoints the server's metadata names, stays on the profile's own hosts.
func authorizeMcp(ctx context.Context, options authorizeMcpOptions) (int, error) {
	options.Request = SecureFetch(contracts.ProfileHostAllowlist(options.Profile.Resource, options.Profile.McpIssuer, options.Profile.Development), options.Request)
	pending, err := options.Marker.Pending()
	if err != nil {
		return 0, err
	}
	if pending {
		if err := RetireKeptGrant(ctx, RetireOptions{Profile: options.Profile, Store: options.Store, Pending: options.Pending, Fetch: options.Request}); err != nil {
			return 0, err
		}
	}
	callback, err := ListenForAuthorization(options.Profile.McpIssuer, 0)
	if err != nil {
		return 0, err
	}
	defer callback.Close()
	provider := NewMcpOAuthProvider(McpOAuthOptions{
		Profile: options.Profile,
		Store:   options.Store,
		Authorization: &McpAuthorization{
			RedirectURL: callback.RedirectURL,
			State:       callback.State,
			Open:        func(ctx context.Context, url *weburl.URL) error { return options.Open(ctx, url.Href()) },
		},
	})
	fetch := HTTPFetch(options.Request)
	warn := ConsoleWarn(os.Stderr)
	result, err := oauth.Auth(ctx, provider, oauth.Options{ServerURL: options.Profile.Resource, Fetch: fetch, Warn: warn})
	if err != nil {
		return 0, err
	}
	if result == oauth.Redirect {
		if err := exchangeCode(ctx, options, provider, callback, fetch, warn); err != nil {
			return 0, err
		}
	}
	if err := options.Marker.Clear(); err != nil {
		return 0, err
	}
	return authenticatedCatalog(ctx, options, provider, fetch, warn)
}

func exchangeCode(ctx context.Context, options authorizeMcpOptions, provider *McpOAuthProvider, callback *LoopbackAuthorization, fetch httpx.Fetch, warn func(string)) error {
	returned, err := callback.Result(ctx)
	if err != nil {
		return err
	}
	auth := oauth.Options{ServerURL: options.Profile.Resource, AuthorizationCode: &returned.Code, Fetch: fetch, Warn: warn}
	if returned.Issuer != "" {
		auth.Iss = &returned.Issuer
	}
	result, err := oauth.Auth(ctx, provider, auth)
	if err != nil {
		return err
	}
	if result != oauth.Authorized {
		return clierror.New("MCP authorization is incomplete. Run durin to resume.")
	}
	return nil
}

func authenticatedCatalog(ctx context.Context, options authorizeMcpOptions, provider *McpOAuthProvider, fetch httpx.Fetch, warn func(string)) (int, error) {
	client, err := NewRemoteMcpClient(RemoteMcpOptions{
		Resource:      options.Profile.Resource,
		Development:   options.Profile.Development,
		Authorization: provider,
		Fetch:         fetch,
		Warn:          warn,
	})
	if err != nil {
		return 0, err
	}
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return 0, err
	}
	catalog, err := client.ListTools(ctx)
	if err != nil {
		return 0, err
	}
	tools, _ := catalog.GetArray("tools")
	return len(tools), nil
}
