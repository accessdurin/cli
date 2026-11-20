package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

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
	noted := &notedIssuers{}
	metadata, err := oauth.DiscoverOAuthProtectedResourceMetadata(ctx, resource, nil, observingProtectedResource(resource, HTTPFetch(request), noted))
	if err != nil {
		return "", err
	}
	issuers := metadata.AuthorizationServers
	if metadata.Resource != resource || len(issuers) != 1 {
		return "", clierror.New("MCP discovery must identify the exact personal resource and one trusted issuer.")
	}
	issuer, err := agreedIssuer(noted, issuers[0], scope)
	if err != nil {
		return "", err
	}
	if issuer == "" || !contracts.SafeCliURL(issuer, scope.Development) {
		return "", clierror.New("Unsafe MCP authorization issuer.")
	}
	if err := assertExpectedHost(contracts.RoleMcpIssuer, issuer, scope); err != nil {
		return "", err
	}
	return contracts.CanonicalIssuer(contracts.RoleMcpIssuer, issuer, scope)
}

// notedIssuers are the issuers named by the protected-resource documents discovery read: the
// last 2xx document on the resource's origin is the one durin pins; earlier ones are others.
type notedIssuers struct {
	pinned string
	others []string
}

const issuersDisagree = "MCP discovery found protected-resource documents that name different sign-in servers. Nothing was saved."

// sameIssuer: one root trailing slash does not make a different issuer. Nothing else is folded in.
func sameIssuer(left, right string, scope contracts.CliHostScope) bool {
	l, lerr := contracts.ComparableIssuer(contracts.RoleMcpIssuer, left, scope)
	r, rerr := contracts.ComparableIssuer(contracts.RoleMcpIssuer, right, scope)
	return lerr == nil && rerr == nil && l == r
}

// agreedIssuer is the SDK's issuer only when it is the one the pinned document named and every
// other document agreed. A pinned issuer on an unexpected host is reported as that.
func agreedIssuer(noted *notedIssuers, sdkIssuer string, scope contracts.CliHostScope) (string, error) {
	if noted.pinned == "" || !sameIssuer(noted.pinned, sdkIssuer, scope) {
		if noted.pinned != "" {
			if err := assertExpectedHost(contracts.RoleMcpIssuer, noted.pinned, scope); err != nil {
				return "", err
			}
		}
		return "", clierror.New(issuersDisagree)
	}
	for _, other := range noted.others {
		if !sameIssuer(other, sdkIssuer, scope) {
			return "", clierror.New(issuersDisagree)
		}
	}
	return sdkIssuer, nil
}

// documentIssuer is the first of a document's authorization_servers, "" when it has none.
func documentIssuer(body []byte) string {
	var document struct {
		AuthorizationServers []any `json:"authorization_servers"`
	}
	if json.Unmarshal(body, &document) != nil || len(document.AuthorizationServers) == 0 {
		return ""
	}
	for _, server := range document.AuthorizationServers {
		if _, ok := server.(string); !ok {
			return ""
		}
	}
	return document.AuthorizationServers[0].(string)
}

func sameOrigin(target, origin string) bool {
	u, err := weburl.Parse(target)
	return err == nil && u.Origin() == origin
}

// pinnedDocument: only a 2xx protected-resource document served on the resource's origin, from
// the address requested (secure fetch refuses redirects), is the issuer durin will pin.
func pinnedDocument(target string, resp *http.Response, origin string) bool {
	return strings.Contains(target, "oauth-protected-resource") && httpx.OK(resp) &&
		(resp.Request == nil || resp.Request.URL.String() == target) && sameOrigin(target, origin)
}

// observingProtectedResource notes the issuer each pinned protected-resource document names,
// leaving the response for discovery to read.
func observingProtectedResource(resource string, fetch httpx.Fetch, noted *notedIssuers) httpx.Fetch {
	origin := ""
	if u, err := weburl.Parse(resource); err == nil {
		origin = u.Origin()
	}
	return func(ctx context.Context, target string, init *httpx.RequestInit) (*http.Response, error) {
		resp, err := fetch(ctx, target, init)
		if err != nil || !pinnedDocument(target, resp, origin) {
			return resp, err
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if issuer := documentIssuer(body); issuer != "" {
			if noted.pinned != "" {
				noted.others = append(noted.others, noted.pinned)
			}
			noted.pinned = issuer
		}
		return resp, nil
	}
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
