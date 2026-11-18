package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/jsurl"
	"github.com/accessdurin/cli/internal/mcp/oauth"
)

// memoryProvider is a CLI-like OAuthClientProvider kept in memory.
type memoryProvider struct {
	mu          sync.Mutex
	redirect    string
	tokens      *oauth.Tokens
	client      *oauth.ClientInformation
	discovery   *oauth.DiscoveryState
	verifier    string
	redirected  *jsurl.URL
	invalidated []oauth.InvalidationScope
}

func (p *memoryProvider) RedirectURL() string { return p.redirect }
func (p *memoryProvider) ClientMetadata() *jsonv.Object {
	return jsonv.NewObject("client_name", "Durin CLI", "application_type", "native",
		"redirect_uris", []any{p.redirect}, "grant_types", []any{"authorization_code", "refresh_token"},
		"response_types", []any{"code"}, "token_endpoint_auth_method", "none")
}
func (p *memoryProvider) State(context.Context) (string, error) { return "the-state", nil }
func (p *memoryProvider) RedirectToAuthorization(_ context.Context, u *jsurl.URL) error {
	p.mu.Lock()
	p.redirected = u
	p.mu.Unlock()
	return nil
}
func (p *memoryProvider) SaveCodeVerifier(_ context.Context, v string) error {
	p.verifier = v
	return nil
}
func (p *memoryProvider) CodeVerifier(context.Context) (string, error) { return p.verifier, nil }
func (p *memoryProvider) ClientInformation(context.Context, *oauth.ClientInformationContext) (*oauth.ClientInformation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.client, nil
}
func (p *memoryProvider) SaveClientInformation(_ context.Context, c oauth.ClientInformation, _ *oauth.ClientInformationContext) error {
	p.mu.Lock()
	p.client = &c
	p.mu.Unlock()
	return nil
}
func (p *memoryProvider) Tokens(context.Context, *oauth.ClientInformationContext) (*oauth.Tokens, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tokens, nil
}
func (p *memoryProvider) SaveTokens(_ context.Context, t oauth.Tokens, _ *oauth.ClientInformationContext) error {
	p.mu.Lock()
	p.tokens = &t
	p.mu.Unlock()
	return nil
}
func (p *memoryProvider) ValidateResourceURL(_ context.Context, u *jsurl.URL, _ *string) (*jsurl.URL, error) {
	return u, nil
}
func (p *memoryProvider) SaveDiscoveryState(_ context.Context, s oauth.DiscoveryState) error {
	p.discovery = &s
	return nil
}
func (p *memoryProvider) DiscoveryState(context.Context) (*oauth.DiscoveryState, error) {
	return p.discovery, nil
}
func (p *memoryProvider) InvalidateCredentials(_ context.Context, scope oauth.InvalidationScope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.invalidated = append(p.invalidated, scope)
	if scope == oauth.InvalidateTokens || scope == oauth.InvalidateAll {
		p.tokens = nil
	}
	return nil
}

type mcpServer struct {
	t            *testing.T
	srv          *httptest.Server
	mu           sync.Mutex
	log          []string
	validToken   string
	callDelay    time.Duration
	cancelled    []string
	refreshCount int
}

func (s *mcpServer) record(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, fmt.Sprintf("%s %s auth=%q sid=%q pv=%q %s", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Mcp-Session-Id"), r.Header.Get("Mcp-Protocol-Version"), body))
}

func newMCPServer(t *testing.T) *mcpServer {
	s := &mcpServer{t: t, validToken: "fresh-token"}
	mux := http.NewServeMux()
	s.srv = httptest.NewServer(mux)
	base := s.srv.URL
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, "")
		fmt.Fprintf(w, `{"resource":%q,"authorization_servers":[%q]}`, base+"/mcp", base+"/")
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		s.record(r, "")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"registration_endpoint":%q,"response_types_supported":["code"],"code_challenge_methods_supported":["S256"]}`, base+"/", base+"/authorize", base+"/token", base+"/register")
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.record(r, string(b))
		w.WriteHeader(201)
		fmt.Fprint(w, `{"client_id":"dcr-client","redirect_uris":["http://127.0.0.1:9/cb"]}`)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.record(r, string(b))
		form, _ := url.ParseQuery(string(b))
		switch form.Get("grant_type") {
		case "authorization_code":
			if oauth.PKCEChallenge(form.Get("code_verifier")) == "" || form.Get("code") != "the-code" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"first-token","token_type":"Bearer","refresh_token":"r1","expires_in":3600}`)
		case "refresh_token":
			s.mu.Lock()
			s.refreshCount++
			s.mu.Unlock()
			fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer"}`, s.validToken)
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.record(r, string(b))
		if r.Header.Get("Authorization") != "Bearer "+s.validToken {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="invalid_token", resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodGet {
			if r.Header.Get("Last-Event-ID") != "" {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"resumed\"}]}}\n\n")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": comment\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			return
		}
		v, _ := jsonv.Parse(b)
		msg := v.(*jsonv.Object)
		method, _ := MessageMethod(msg)
		id, hasID := MessageID(msg)
		idJSON := jsonv.MustStringify(id)
		if !hasID {
			if method == "notifications/cancelled" {
				s.mu.Lock()
				s.cancelled = append(s.cancelled, string(b))
				s.mu.Unlock()
			}
			w.WriteHeader(202)
			return
		}
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-42")
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"e2e","version":"1"}}}`, idJSON)
		case "tools/list":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "id: 1\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"tools\":[{\"name\":\"t\",\"inputSchema\":{\"type\":\"object\"}}]}}\n\n", idJSON)
		case "tools/call":
			name, _ := jsonv.Path(msg, "params", "name")
			switch name {
			case "slow":
				time.Sleep(s.callDelay)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[]}}`, idJSON)
			case "resumable":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "retry: 5\nid: ev-1\ndata: \n\n")
			default:
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}]}}`, idJSON)
			}
		}
	})
	t.Cleanup(s.srv.Close)
	return s
}

func (s *mcpServer) entries(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.log {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// TestEndToEndOAuthAndSession drives a real HTTP server through the full flow: discovery, DCR,
// PKCE redirect, code exchange, then an MCP session whose first request is refused (401), is
// refreshed once and retried; session id and protocol version headers follow; SSE responses and
// the standalone GET stream deliver messages.
func TestEndToEndOAuthAndSession(t *testing.T) {
	s := newMCPServer(t)
	fetch := httpx.NewHTTPFetch(s.srv.Client())
	ctx := context.Background()
	p := &memoryProvider{redirect: "http://127.0.0.1:9/cb"}
	res, err := oauth.Auth(ctx, p, oauth.Options{ServerURL: s.srv.URL + "/mcp", Fetch: fetch})
	if err != nil || res != oauth.Redirect {
		t.Fatalf("auth = %v, %v", res, err)
	}
	q := p.redirected.SearchParams()
	for k, want := range map[string]string{"response_type": "code", "client_id": "dcr-client", "code_challenge_method": "S256", "redirect_uri": "http://127.0.0.1:9/cb", "state": "the-state", "resource": s.srv.URL + "/mcp"} {
		if got, _ := q.Get(k); got != want {
			t.Errorf("authorization URL %s = %q, want %q", k, got, want)
		}
	}
	if ch, _ := q.Get("code_challenge"); ch != oauth.PKCEChallenge(p.verifier) {
		t.Errorf("code_challenge does not match the verifier")
	}
	code := "the-code"
	res, err = oauth.Auth(ctx, p, oauth.Options{ServerURL: s.srv.URL + "/mcp", Fetch: fetch, AuthorizationCode: &code})
	if err != nil || res != oauth.Authorized {
		t.Fatalf("code exchange = %v, %v", res, err)
	}
	if p.tokens.AccessToken != "first-token" || *p.tokens.Issuer != s.srv.URL+"/" || *p.client.Issuer != s.srv.URL+"/" {
		t.Fatalf("stored tokens %+v client %+v", p.tokens, p.client)
	}

	tr, err := NewStreamableHTTPClientTransport(jsurl.MustParse(s.srv.URL+"/mcp"), StreamableHTTPOptions{
		OAuthProvider: p, Fetch: fetch, RequestInit: &httpx.RequestInit{Redirect: httpx.RedirectError}, OnInsufficientScope: "throw",
	})
	if err != nil {
		t.Fatal(err)
	}
	notes := make(chan Message, 4)
	tr.SetHandlers(Handlers{OnMessage: func(m Message) {
		if method, ok := MessageMethod(m); ok && strings.HasPrefix(method, "notifications/") {
			notes <- m
		}
	}})
	client := NewClient(Implementation{Name: "Durin CLI", Version: "1.0.0"}, ClientOptions{ListMaxPages: 64})
	if err := client.Connect(ctx, tr, RequestOptions{Timeout: 5 * time.Second}); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if tr.SessionID() != "session-42" || client.NegotiatedProtocolVersion() != "2025-06-18" {
		t.Fatalf("session %q version %q", tr.SessionID(), client.NegotiatedProtocolVersion())
	}
	select {
	case m := <-notes:
		if method, _ := MessageMethod(m); method != "notifications/tools/list_changed" {
			t.Fatalf("standalone stream delivered %s", jsonv.MustStringify(m))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("standalone GET stream delivered nothing")
	}
	tools, err := client.ListTools(ctx, nil, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonv.MustStringify(tools); got != `{"tools":[{"name":"t","inputSchema":{"type":"object"}}]}` {
		t.Fatalf("tools = %s", got)
	}
	result, err := client.CallTool(ctx, CallToolParams{Name: "echo", Arguments: jsonv.NewObject()}, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonv.MustStringify(result); got != `{"content":[{"type":"text","text":"ok"}]}` {
		t.Fatalf("result = %s", got)
	}
	posts := s.entries("POST /mcp")
	if !strings.Contains(posts[0], `auth="Bearer first-token"`) || !strings.Contains(posts[1], `auth="Bearer fresh-token"`) {
		t.Fatalf("expected a 401 then a retry with the refreshed token:\n%s", strings.Join(posts, "\n"))
	}
	if !strings.Contains(posts[1], `sid=""`) || !strings.Contains(posts[2], `sid="session-42" pv="2025-06-18"`) {
		t.Fatalf("session headers:\n%s", strings.Join(posts, "\n"))
	}
	if s.refreshCount != 1 {
		t.Fatalf("refreshed %d times", s.refreshCount)
	}
}

func connectedClient(t *testing.T, s *mcpServer) (*Client, *StreamableHTTPClientTransport) {
	t.Helper()
	p := &memoryProvider{redirect: "http://127.0.0.1:9/cb", tokens: &oauth.Tokens{AccessToken: "fresh-token", TokenType: "Bearer"}}
	tr, err := NewStreamableHTTPClientTransport(jsurl.MustParse(s.srv.URL+"/mcp"), StreamableHTTPOptions{OAuthProvider: p, Fetch: httpx.NewHTTPFetch(s.srv.Client())})
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(Implementation{Name: "x", Version: "1"}, ClientOptions{})
	if err := c.Connect(context.Background(), tr, RequestOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, tr
}

// TestRequestTimeoutCancels: a request that outlives its timeout fails with RequestTimeout and
// the server receives notifications/cancelled naming the request.
func TestRequestTimeoutCancels(t *testing.T) {
	s := newMCPServer(t)
	s.callDelay = 300 * time.Millisecond
	c, _ := connectedClient(t, s)
	_, err := c.CallTool(context.Background(), CallToolParams{Name: "slow"}, RequestOptions{Timeout: 50 * time.Millisecond})
	var se *SdkError
	if !errors.As(err, &se) || se.Code != ErrRequestTimeout || se.Message != "Request timed out" || se.Data["timeout"] != int64(50) {
		t.Fatalf("err = %#v", err)
	}
	time.Sleep(100 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cancelled) != 1 || s.cancelled[0] != `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"SdkError: Request timed out"}}` {
		t.Fatalf("cancellations %q", s.cancelled)
	}
}

// TestSSEResumption: a POST stream that sends only a priming event and ends is resumed with a GET
// carrying Last-Event-ID, after the server's retry interval, and the response arrives there.
func TestSSEResumption(t *testing.T) {
	s := newMCPServer(t)
	c, _ := connectedClient(t, s)
	result, err := c.CallTool(context.Background(), CallToolParams{Name: "resumable"}, RequestOptions{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonv.MustStringify(result); got != `{"content":[{"type":"text","text":"resumed"}]}` {
		t.Fatalf("result %s", got)
	}
}

// TestRedirectsAreErrors: with requestInit redirect 'error' a redirecting MCP endpoint fails the
// request instead of being followed.
func TestRedirectsAreErrors(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect was followed to %s", r.URL)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/mcp", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	tr, _ := NewStreamableHTTPClientTransport(jsurl.MustParse(srv.URL+"/mcp"), StreamableHTTPOptions{
		AuthProvider: staticToken("x"), Fetch: httpx.NewHTTPFetch(srv.Client()), RequestInit: &httpx.RequestInit{Redirect: httpx.RedirectError},
	})
	c := NewClient(Implementation{Name: "x", Version: "1"}, ClientOptions{})
	err := c.Connect(context.Background(), tr, RequestOptions{Timeout: time.Second})
	if !httpx.IsTypeError(err) {
		t.Fatalf("err = %v", err)
	}
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

func TestNewTransportRequiresFetch(t *testing.T) {
	if _, err := NewStreamableHTTPClientTransport(jsurl.MustParse("https://x/mcp"), StreamableHTTPOptions{}); err == nil {
		t.Fatal("a transport without Fetch was created")
	}
}

// TestStdioClientTransport spawns this test binary as an MCP server (see TestHelperStdioServer):
// connect, list tools, close. The child sees only the default environment plus Env.
func TestStdioClientTransport(t *testing.T) {
	t.Setenv("MCP_PORT_SECRET", "must-not-leak")
	tr := NewStdioClientTransport(StdioServerParameters{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperStdioServer"},
		Env:     map[string]string{"GO_MCP_HELPER": "1"},
		Stderr:  "inherit",
	})
	closed := make(chan struct{})
	c := NewClient(Implementation{Name: "Durin setup verification", Version: "1.0.0"}, ClientOptions{})
	c.OnClose = func() { close(closed) }
	if err := c.Connect(context.Background(), tr, RequestOptions{Timeout: 15 * time.Second}); err != nil {
		t.Fatal(err)
	}
	tools, err := c.ListTools(context.Background(), nil, RequestOptions{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	got := jsonv.MustStringify(tools)
	if strings.Contains(got, "MCP_PORT_SECRET") || !strings.Contains(got, `"name":"GO_MCP_HELPER"`) || !strings.Contains(got, `"name":"PATH"`) {
		t.Fatalf("child environment: %s", got)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("onclose did not fire")
	}
}

// TestHelperStdioServer is not a real test: run as a child process, it answers initialize and
// tools/list (one tool per environment variable name) over stdio.
func TestHelperStdioServer(t *testing.T) {
	if os.Getenv("GO_MCP_HELPER") != "1" {
		t.Skip("helper process")
	}
	tr := NewStdioServerTransport(os.Stdin, os.Stdout, StdioServerOptions{MaxBufferSize: 1_048_576})
	done := make(chan struct{})
	tr.SetHandlers(Handlers{
		OnClose: func() { close(done) },
		OnMessage: func(m Message) {
			method, _ := MessageMethod(m)
			id, ok := MessageID(m)
			if !ok {
				return
			}
			var result *jsonv.Object
			switch method {
			case "initialize":
				result = jsonv.NewObject("protocolVersion", "2025-11-25", "capabilities", jsonv.NewObject("tools", jsonv.NewObject()), "serverInfo", jsonv.NewObject("name", "helper", "version", "1"))
			case "tools/list":
				var tools []any
				for _, kv := range os.Environ() {
					k, _, _ := strings.Cut(kv, "=")
					tools = append(tools, jsonv.NewObject("name", k, "inputSchema", jsonv.NewObject("type", "object")))
				}
				result = jsonv.NewObject("tools", tools)
			default:
				return
			}
			_ = tr.Send(context.Background(), jsonv.NewObject("jsonrpc", "2.0", "id", id, "result", result), nil)
		},
	})
	_ = tr.Start(context.Background())
	<-done
	os.Exit(0)
}
