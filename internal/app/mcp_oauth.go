package app

import (
	"context"
	"sync"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/mcp/schema"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// SignInAgain also covers a grant the server revoked: the SDK deletes its tokens when a
// refresh is refused.
const SignInAgain = "This durin profile isn’t signed in to its organization’s MCP server: it never signed in, or its sign-in ended or was revoked. Run durin in a terminal to sign in again."

func signInRequired() error { return clierror.New(SignInAgain) }

// McpAuthorization is an interactive sign-in: the loopback redirect URL, the OAuth state it
// expects, and how to open the authorization URL.
type McpAuthorization struct {
	RedirectURL string
	State       string
	Open        func(ctx context.Context, url *weburl.URL) error
}

// McpOAuthOptions configure an McpOAuthProvider. Authorization is nil outside sign-in. Marker
// is given for MCP sessions, which must not use or refresh the grant while sign-out is
// unfinished; nil for none.
type McpOAuthOptions struct {
	Profile       contracts.CliProfile
	Store         ports.CredentialStore
	Authorization *McpAuthorization
	Marker        ports.RevocationMarker
}

type credentialKind string

const (
	clientCredential credentialKind = "client"
	tokensCredential credentialKind = "tokens"
)

var (
	issuerBindingSchema  = zod.Object(zod.F("issuer", zod.String()))
	clientMetadataSchema = zod.Object(
		zod.F("redirect_uris", zod.Optional(zod.Array(contracts.URL()))),
		zod.F("token_endpoint_auth_method", zod.Optional(zod.Literal(jsjson.String("none")))),
	)
)

func registeredRedirects(value jsjson.Value) ([]string, error) {
	out, err := zod.Parse(clientMetadataSchema, value)
	if err != nil {
		return nil, err
	}
	uris, _ := out.(*jsjson.Object).GetArray("redirect_uris")
	list := make([]string, len(uris))
	for i, uri := range uris {
		list[i] = string(uri.(jsjson.String))
	}
	return list, nil
}

func requireIssuer(value jsjson.Value, issuer string) (string, error) {
	out, err := zod.Parse(issuerBindingSchema, value)
	if err != nil {
		return "", err
	}
	if bound, _ := out.(*jsjson.Object).GetString("issuer"); bound != issuer {
		return "", clierror.New("MCP credential issuer mismatch.")
	}
	return issuer, nil
}

// parseTokens is {...OAuthTokensSchema.parse(value), issuer: requireIssuer(value, issuer)}.
func parseTokens(value jsjson.Value, issuer string) (*jsjson.Object, error) {
	out, err := schema.Parse(oauth.TokensSchema, value)
	if err != nil {
		return nil, err
	}
	bound, err := requireIssuer(value, issuer)
	if err != nil {
		return nil, err
	}
	return out.(*jsjson.Object).Clone().Set("issuer", jsjson.String(bound)), nil
}

// parseClient is {...OAuthClientInformationSchema.parse(value),
// ...clientMetadataSchema.parse(value), issuer: requireIssuer(value, issuer)}.
func parseClient(value jsjson.Value, issuer string) (*jsjson.Object, error) {
	info, err := schema.Parse(oauth.ClientInformationSchema, value)
	if err != nil {
		return nil, err
	}
	metadata, err := zod.Parse(clientMetadataSchema, value)
	if err != nil {
		return nil, err
	}
	bound, err := requireIssuer(value, issuer)
	if err != nil {
		return nil, err
	}
	return info.(*jsjson.Object).Clone().Assign(metadata.(*jsjson.Object)).Set("issuer", jsjson.String(bound)), nil
}

// authorizationURL: authorization-server metadata chooses this URL; only web URLs may reach
// the OS opener.
func authorizationURL(url *weburl.URL, development bool) (*weburl.URL, error) {
	base := url.Clone()
	base.SetSearch("")
	if !contracts.SafeCliURL(base.Href(), development) {
		return nil, clierror.New("Unsafe MCP authorization URL.")
	}
	return url, nil
}

// McpOAuthProvider is McpOAuthProvider (src/mcp-oauth.ts): the MCP SDK's OAuth client
// provider for one profile. Its client registration and tokens live in the OS credential
// store as mcp:<profileId>:client and mcp:<profileId>:tokens, each bound to the profile's
// MCP issuer.
type McpOAuthProvider struct {
	options McpOAuthOptions

	mu        sync.Mutex
	verifier  string
	discovery *oauth.DiscoveryState
}

var _ oauth.ClientProvider = (*McpOAuthProvider)(nil)

// NewMcpOAuthProvider returns the provider for options.Profile.
func NewMcpOAuthProvider(options McpOAuthOptions) *McpOAuthProvider {
	return &McpOAuthProvider{options: options}
}

// RedirectURL is the sign-in's loopback redirect, or a placeholder outside sign-in.
func (p *McpOAuthProvider) RedirectURL() string {
	if p.options.Authorization != nil {
		return p.options.Authorization.RedirectURL
	}
	return "http://127.0.0.1/oauth/callback"
}

// ClientMetadata is the registration request for a public native client.
func (p *McpOAuthProvider) ClientMetadata() *jsjson.Object {
	return jsjson.Obj(
		jsjson.KV("client_name", jsjson.String("Durin CLI")),
		jsjson.KV("application_type", jsjson.String("native")),
		jsjson.KV("redirect_uris", jsjson.Strings(p.RedirectURL())),
		jsjson.KV("grant_types", jsjson.Strings("authorization_code", "refresh_token")),
		jsjson.KV("response_types", jsjson.Strings("code")),
		jsjson.KV("token_endpoint_auth_method", jsjson.String("none")),
	)
}

func (p *McpOAuthProvider) authorization() (*McpAuthorization, error) {
	if p.options.Authorization == nil {
		return nil, signInRequired()
	}
	return p.options.Authorization, nil
}

// State is the sign-in's OAuth state; outside sign-in, a new sign-in is required.
func (p *McpOAuthProvider) State(context.Context) (string, error) {
	authorization, err := p.authorization()
	if err != nil {
		return "", err
	}
	return authorization.State, nil
}

// RedirectToAuthorization opens url once it is known to be a safe web URL.
func (p *McpOAuthProvider) RedirectToAuthorization(ctx context.Context, url *weburl.URL) error {
	authorization, err := p.authorization()
	if err != nil {
		return err
	}
	safe, err := authorizationURL(url, p.options.Profile.Development)
	if err != nil {
		return err
	}
	return authorization.Open(ctx, safe)
}

// SaveCodeVerifier keeps the PKCE verifier in memory only.
func (p *McpOAuthProvider) SaveCodeVerifier(_ context.Context, verifier string) error {
	p.mu.Lock()
	p.verifier = verifier
	p.mu.Unlock()
	return nil
}

// CodeVerifier returns the saved verifier; without one, a new sign-in is required.
func (p *McpOAuthProvider) CodeVerifier(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier == "" {
		return "", signInRequired()
	}
	return p.verifier, nil
}

// ClientInformation returns the stored registration. Outside sign-in a missing registration
// requires a new sign-in; during sign-in a registration is reused only while it holds tokens
// or lists the current redirect URL.
func (p *McpOAuthProvider) ClientInformation(ctx context.Context, info *oauth.ClientInformationContext) (*oauth.ClientInformation, error) {
	client, err := p.load(clientCredential, parseClient, info)
	if err != nil {
		return nil, err
	}
	if client == nil && p.options.Authorization == nil {
		return nil, signInRequired()
	}
	usable, err := p.clientForAuthorization(ctx, client)
	if err != nil || usable == nil {
		return nil, err
	}
	return oauth.ClientInformationFromObject(usable), nil
}

// SaveClientInformation stores a registration bound to the profile's MCP issuer.
func (p *McpOAuthProvider) SaveClientInformation(_ context.Context, client oauth.ClientInformation, info *oauth.ClientInformationContext) error {
	if err := p.assertIssuer(info); err != nil {
		return err
	}
	value, err := client.Object()
	if err != nil {
		return err
	}
	parsed, err := parseClient(value, p.options.Profile.McpIssuer)
	if err != nil {
		return err
	}
	return p.options.Store.Write(p.key(clientCredential), jsjson.Stringify(parsed))
}

// Tokens returns the stored tokens. An MCP session refuses them while sign-out is unfinished.
func (p *McpOAuthProvider) Tokens(_ context.Context, info *oauth.ClientInformationContext) (*oauth.Tokens, error) {
	if p.options.Marker != nil {
		if err := AssertSignOutFinished(p.options.Marker); err != nil {
			return nil, err
		}
	}
	tokens, err := p.load(tokensCredential, parseTokens, info)
	if err != nil || tokens == nil {
		return nil, err
	}
	return oauth.ParseTokens(tokens)
}

// SaveTokens stores tokens bound to the profile's MCP issuer.
func (p *McpOAuthProvider) SaveTokens(_ context.Context, tokens oauth.Tokens, info *oauth.ClientInformationContext) error {
	if err := p.assertIssuer(info); err != nil {
		return err
	}
	parsed, err := parseTokens(tokens.Object(), p.options.Profile.McpIssuer)
	if err != nil {
		return err
	}
	return p.options.Store.Write(p.key(tokensCredential), jsjson.Stringify(parsed))
}

// ValidateResourceURL accepts only the profile's own resource, both as the server URL and as
// the protected-resource metadata names it.
func (p *McpOAuthProvider) ValidateResourceURL(_ context.Context, serverURL *weburl.URL, resource *string) (*weburl.URL, error) {
	own := p.options.Profile.Resource
	if serverURL.Href() != own || resource == nil || *resource != own {
		return nil, clierror.New("MCP resource does not match the selected organization profile.")
	}
	return weburl.Parse(own)
}

// SaveDiscoveryState accepts only the profile's MCP issuer, compared with ComparableIssuer.
func (p *McpOAuthProvider) SaveDiscoveryState(_ context.Context, state oauth.DiscoveryState) error {
	profile := p.options.Profile
	scope := contracts.CliHostScope{AppOrigin: profile.AppOrigin, Development: profile.Development, AccountIssuer: profile.AccountIssuer}
	// The SDK names the sign-in server as the metadata spells it. One root trailing slash is the
	// same issuer, including for a self-hosted app; case, port, //, a path and another host are not.
	discovered, err := contracts.ComparableIssuer(contracts.RoleMcpIssuer, state.AuthorizationServerURL, scope)
	if err != nil {
		return err
	}
	expected, err := contracts.ComparableIssuer(contracts.RoleMcpIssuer, profile.McpIssuer, scope)
	if err != nil {
		return err
	}
	if discovered != expected {
		return clierror.New("MCP authorization issuer does not match the selected profile.")
	}
	p.mu.Lock()
	p.discovery = &state
	p.mu.Unlock()
	return nil
}

// DiscoveryState returns the state saved in this process, if any.
func (p *McpOAuthProvider) DiscoveryState(context.Context) (*oauth.DiscoveryState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.discovery, nil
}

// InvalidateCredentials deletes the stored credentials and in-memory state that scope names.
func (p *McpOAuthProvider) InvalidateCredentials(_ context.Context, scope oauth.InvalidationScope) error {
	var kinds []credentialKind
	switch scope {
	case oauth.InvalidateAll:
		kinds = []credentialKind{clientCredential, tokensCredential}
	case oauth.InvalidateClient:
		kinds = []credentialKind{clientCredential}
	case oauth.InvalidateTokens:
		kinds = []credentialKind{tokensCredential}
	}
	errs := make([]error, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = p.options.Store.Remove(p.key(kind))
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	p.mu.Lock()
	if scope == oauth.InvalidateAll || scope == oauth.InvalidateVerifier {
		p.verifier = ""
	}
	if scope == oauth.InvalidateAll || scope == oauth.InvalidateDiscovery {
		p.discovery = nil
	}
	p.mu.Unlock()
	return nil
}

func (p *McpOAuthProvider) clientForAuthorization(ctx context.Context, client *jsjson.Object) (*jsjson.Object, error) {
	if p.options.Authorization == nil || client == nil {
		return client, nil
	}
	tokens, err := p.Tokens(ctx, nil)
	if err != nil {
		return nil, err
	}
	redirects, err := registeredRedirects(client)
	if err != nil {
		return nil, err
	}
	if tokens != nil {
		return client, nil
	}
	for _, uri := range redirects {
		if uri == p.RedirectURL() {
			return client, nil
		}
	}
	return nil, nil
}

func (p *McpOAuthProvider) key(kind credentialKind) string {
	return "mcp:" + p.options.Profile.ID + ":" + string(kind)
}

func (p *McpOAuthProvider) assertIssuer(info *oauth.ClientInformationContext) error {
	if info != nil && info.Issuer != p.options.Profile.McpIssuer {
		return clierror.New("MCP authorization issuer does not match the selected profile.")
	}
	return nil
}

// load reads and parses one credential; nil when it is absent. Anything stored that does not
// parse, or names another issuer, is reported as invalid.
func (p *McpOAuthProvider) load(kind credentialKind, parse func(jsjson.Value, string) (*jsjson.Object, error), info *oauth.ClientInformationContext) (*jsjson.Object, error) {
	if err := p.assertIssuer(info); err != nil {
		return nil, err
	}
	raw, found, err := p.options.Store.Read(p.key(kind))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	value, err := jsjson.ParseString(raw)
	if err == nil {
		var parsed *jsjson.Object
		if parsed, err = parse(value, p.options.Profile.McpIssuer); err == nil {
			return parsed, nil
		}
	}
	return nil, clierror.New("Saved MCP credentials are invalid. Run durin to authorize this organization again.")
}
