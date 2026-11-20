package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/weburl"
)

type memoryMarker struct{ pending bool }

func (m *memoryMarker) Pending() (bool, error) { return m.pending, nil }
func (m *memoryMarker) Mark() error            { m.pending = true; return nil }
func (m *memoryMarker) Clear() error           { m.pending = false; return nil }

func ptrTo[T any](v T) *T { return &v }

// signInGrant stores the client and tokens a finished sign-in leaves for profile.
func signInGrant(t *testing.T, provider *McpOAuthProvider, issuer string) {
	t.Helper()
	ctx := context.Background()
	client := oauth.ClientInformation{ClientID: "client_one", RedirectURIs: []string{"http://127.0.0.1:1111/oauth/callback"}, TokenEndpointAuthMethod: ptrTo("none"), ClientName: ptrTo("Durin CLI"), Issuer: &issuer}
	if err := provider.SaveClientInformation(ctx, client, &oauth.ClientInformationContext{Issuer: issuer}); err != nil {
		t.Fatal(err)
	}
	tokens := oauth.Tokens{AccessToken: "synthetic-access", TokenType: "Bearer", ExpiresIn: ptrTo(3600.0), RefreshToken: ptrTo("synthetic-refresh"), Issuer: &issuer}
	if err := provider.SaveTokens(ctx, tokens, &oauth.ClientInformationContext{Issuer: issuer}); err != nil {
		t.Fatal(err)
	}
}

func TestMcpOAuthProviderKeepsIssuerBoundCredentialsInTheStore(t *testing.T) {
	ctx := context.Background()
	profile := mustProfile(t, testScope)
	store := newMemoryStore()
	provider := NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store})
	signInGrant(t, provider, profile.McpIssuer)

	// The same entries and JSON shapes as the TypeScript CLI: registration metadata other than
	// the redirect URIs and auth method is dropped, and both entries carry the issuer.
	issuer := `"issuer":"https://app.example.test/"`
	if got, want := store.values["mcp:"+profile.ID+":client"], `{"client_id":"client_one",`+issuer+`,"redirect_uris":["http://127.0.0.1:1111/oauth/callback"],"token_endpoint_auth_method":"none"}`; got != want {
		t.Errorf("client entry\n got %s\nwant %s", got, want)
	}
	if got, want := store.values["mcp:"+profile.ID+":tokens"], `{"access_token":"synthetic-access","token_type":"Bearer","expires_in":3600,"refresh_token":"synthetic-refresh",`+issuer+`}`; got != want {
		t.Errorf("tokens entry\n got %s\nwant %s", got, want)
	}
	if tokens, err := provider.Tokens(ctx, nil); err != nil || tokens.AccessToken != "synthetic-access" {
		t.Fatalf("tokens = %v, %v", tokens, err)
	}

	// Credentials are never read for, or written for, another authorization server.
	other := &oauth.ClientInformationContext{Issuer: "https://other.example.test"}
	if _, err := provider.Tokens(ctx, other); err == nil {
		t.Error("read tokens for another issuer")
	}
	if err := provider.SaveTokens(ctx, oauth.Tokens{AccessToken: "x", TokenType: "Bearer", Issuer: &other.Issuer}, other); err == nil {
		t.Error("saved tokens for another issuer")
	}
	err := provider.SaveDiscoveryState(ctx, oauth.DiscoveryState{AuthorizationServerURL: "https://other.example.test/"})
	wantCliError(t, err, "MCP authorization issuer does not match the selected profile.")
	// One root trailing slash is the same issuer, a self-hosted one included. Nothing else is.
	for _, discovered := range []string{"https://app.example.test//", "https://app.example.test/x", "HTTPS://app.example.test/", "https://app.example.test:443", "https://other.example.test/"} {
		err = provider.SaveDiscoveryState(ctx, oauth.DiscoveryState{AuthorizationServerURL: discovered})
		wantCliError(t, err, "MCP authorization issuer does not match the selected profile.")
	}
	for _, discovered := range []string{"https://app.example.test", profile.McpIssuer} {
		if err := provider.SaveDiscoveryState(ctx, oauth.DiscoveryState{AuthorizationServerURL: discovered}); err != nil {
			t.Errorf("refused %s as the profile's own issuer: %v", discovered, err)
		}
	}
	for _, resource := range []*string{nil, ptrTo("https://app.example.test/mcp/u/" + strings.Repeat("b", 64))} {
		server, _ := weburl.Parse(profile.Resource)
		_, err := provider.ValidateResourceURL(ctx, server, resource)
		wantCliError(t, err, "MCP resource does not match")
	}

	// A stored entry that does not parse, or names another issuer, is never used.
	for _, stored := range []string{"not json", `{"access_token":"a","token_type":"Bearer","issuer":"https://other.example.test"}`, `{"access_token":"a","token_type":"Bearer"}`} {
		store.values["mcp:"+profile.ID+":tokens"] = stored
		_, err := provider.Tokens(ctx, nil)
		wantCliError(t, err, "Saved MCP credentials are invalid.")
	}

	// Invalidation removes only this profile's entries.
	store.values["mcp:other:tokens"] = "kept"
	if err := provider.InvalidateCredentials(ctx, oauth.InvalidateAll); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 1 {
		t.Errorf("left %v", store.values)
	}
}

func TestMcpOAuthProviderOutsideSignIn(t *testing.T) {
	ctx := context.Background()
	profile := mustProfile(t, testScope)
	marker := &memoryMarker{}
	provider := NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: newMemoryStore(), Marker: marker})
	// Without a registration, or when the SDK would start an interactive sign-in, the user is
	// told to sign in again.
	_, err := provider.ClientInformation(ctx, nil)
	wantCliError(t, err, SignInAgain)
	_, err = provider.State(ctx)
	wantCliError(t, err, SignInAgain)
	_, err = provider.CodeVerifier(ctx)
	wantCliError(t, err, SignInAgain)

	// While sign-out is unfinished, an MCP session cannot use or refresh the grant.
	signInGrant(t, provider, profile.McpIssuer)
	marker.pending = true
	_, err = provider.Tokens(ctx, nil)
	wantCliError(t, err, SignOutUnfinished)
}

func TestMcpOAuthProviderDuringSignIn(t *testing.T) {
	ctx := context.Background()
	profile := mustProfile(t, testScope)
	store := newMemoryStore()
	signInGrant(t, NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store}), profile.McpIssuer)
	delete(store.values, "mcp:"+profile.ID+":tokens")

	var opened []string
	authorization := &McpAuthorization{RedirectURL: "http://127.0.0.1:2222/oauth/callback", State: "state", Open: func(_ context.Context, u *weburl.URL) error {
		opened = append(opened, u.Href())
		return nil
	}}
	provider := NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store, Authorization: authorization})
	// A native registration bound to a previous callback port is not reused.
	if client, err := provider.ClientInformation(ctx, nil); err != nil || client != nil {
		t.Fatalf("reused a registration for another redirect: %v, %v", client, err)
	}
	authorization.RedirectURL = "http://127.0.0.1:1111/oauth/callback"
	if client, err := provider.ClientInformation(ctx, nil); err != nil || client == nil || client.ClientID != "client_one" {
		t.Fatalf("registration for this redirect = %v, %v", client, err)
	}

	// Only web URLs reach the OS opener.
	for _, href := range []string{"file:///etc/passwd", "javascript:alert(1)", "http://app.example.test/authorize?x=1"} {
		u, _ := weburl.Parse(href)
		wantCliError(t, provider.RedirectToAuthorization(ctx, u), "Unsafe MCP authorization URL.")
	}
	u, _ := weburl.Parse("https://app.example.test/authorize?client_id=client_one")
	if err := provider.RedirectToAuthorization(ctx, u); err != nil || len(opened) != 1 || opened[0] != u.Href() {
		t.Fatalf("opened %v, %v", opened, err)
	}
}

func TestConsoleWarnKeepsOnlyLineLayout(t *testing.T) {
	var stderr bytes.Buffer
	ConsoleWarn(&stderr)("[mcp-sdk] Cause: \"a\x1b[31mred\x1b]8;;x\x07\tb\r\nc\u0085d‮e\"")
	// The output of Node's sanitizeConsole for the same message.
	if got, want := stderr.String(), "[mcp-sdk] Cause: \"ared\tb\ncde\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
