package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
)

// resourceHex names the personal MCP resource every scenario uses.
const resourceHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Tool is one entry the fake MCP server lists.
type Tool = map[string]any

// CallReply is how the fake MCP server answers one tools/call.
type CallReply struct {
	Result map[string]any // JSON-RPC result
	Error  map[string]any // JSON-RPC error instead of a result
}

// FakeServer emulates, on one loopback origin, the durin app (/auth/cli/config, /api/v1/...),
// the WorkOS user-management API, an OAuth authorization server and the personal MCP resource.
// Every request is logged so that scenarios can compare what each CLI implementation sent.
type FakeServer struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []LoggedRequest

	// MCP behaviour.
	SSE            bool                                  // answer JSON-RPC requests as text/event-stream
	Pages          [][]Tool                              // tools/list pages
	CallReplies    map[string][]CallReply                // queued replies per tool name
	AcceptedTokens map[string]bool                       // bearer tokens the MCP resource accepts
	MCPHook        func(method string, body []byte) bool // optional override; true = handled with 500

	// OAuth behaviour.
	Revocation       bool // advertise a revocation endpoint
	RevocationStatus int  // status the revocation endpoint answers (default 200)
	RefreshStatus    int  // status of refresh_token grants (default 200)
	tokenCounter     int
	clientCounter    int
	codes            map[string]string // code -> redirect_uri

	// App / account behaviour.
	Principal     string
	Organizations []map[string]any
	MCPStatus     string // ready | needs_setup | unconfigured
	pendingPolls  int
	DevicePending int // authorization_pending answers before the device grant succeeds
}

// LoggedRequest is the part of a request that the CLI chose, independent of HTTP client details.
type LoggedRequest struct {
	Method  string
	Path    string
	Query   string
	Headers map[string]string
	Body    string
}

func NewFakeServer(t *testing.T) *FakeServer {
	t.Helper()
	f := &FakeServer{
		t:              t,
		CallReplies:    map[string][]CallReply{},
		AcceptedTokens: map[string]bool{"access-seeded": true},
		codes:          map[string]string{},
		Principal:      "user_01",
		Organizations:  []map[string]any{{"id": "org_wos_1", "name": "Acme"}},
		MCPStatus:      "ready",
		Revocation:     true,
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *FakeServer) Origin() string   { return f.server.URL }
func (f *FakeServer) Resource() string { return f.server.URL + "/mcp/u/" + resourceHex }

// Requests returns the logged requests so far.
func (f *FakeServer) Requests() []LoggedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LoggedRequest(nil), f.requests...)
}

// ResetLog forgets logged requests, so a scenario can record only one command's traffic.
func (f *FakeServer) ResetLog() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

var loggedHeaders = []string{"Authorization", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-Id", "Origin", "X-Requested-With"}

func (f *FakeServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	entry := LoggedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Headers: map[string]string{}}
	for _, name := range loggedHeaders {
		if value := r.Header.Get(name); value != "" {
			entry.Headers[name] = value
		}
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		entry.Headers["Content-Type"] = strings.TrimSpace(strings.Split(ct, ";")[0])
	}
	entry.Body = normalizedBody(entry.Headers["Content-Type"], body)
	f.mu.Lock()
	f.requests = append(f.requests, entry)
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/mcp/u/"+resourceHex:
		f.serveMCP(w, r, body)
	case r.URL.Path == "/.well-known/oauth-protected-resource/mcp/u/"+resourceHex:
		writeJSON(w, 200, map[string]any{"resource": f.Resource(), "authorization_servers": []string{f.Origin()}})
	case r.URL.Path == "/.well-known/oauth-authorization-server":
		f.serveMetadata(w)
	case r.URL.Path == "/register":
		f.serveRegister(w, body)
	case r.URL.Path == "/authorize":
		f.serveAuthorize(w, r)
	case r.URL.Path == "/token":
		f.serveToken(w, body)
	case r.URL.Path == "/revoke":
		status := f.RevocationStatus
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
	case r.URL.Path == "/auth/cli/config":
		writeJSON(w, 200, map[string]any{"clientId": "client_account", "issuer": f.Origin(), "providerOrigin": f.Origin(), "appOrigin": f.Origin()})
	case strings.HasPrefix(r.URL.Path, "/user_management/"):
		f.serveWorkOS(w, r.URL.Path, body)
	case strings.HasPrefix(r.URL.Path, "/api/v1/"):
		f.serveAccount(w, r)
	default:
		http.NotFound(w, r)
	}
}

// normalizedBody renders JSON with sorted keys and forms with sorted fields, so field order
// (an HTTP-client detail) never makes two implementations look different.
func normalizedBody(contentType string, body []byte) string {
	if len(body) == 0 {
		return ""
	}
	switch contentType {
	case "application/json":
		var value any
		if json.Unmarshal(body, &value) == nil {
			out, _ := json.Marshal(value) // encoding/json sorts map keys
			return string(out)
		}
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err == nil {
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, key+"="+strings.Join(values[key], ","))
			}
			return strings.Join(parts, "&")
		}
	}
	return string(body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *FakeServer) serveMetadata(w http.ResponseWriter) {
	metadata := map[string]any{
		"issuer":                                f.Origin(),
		"authorization_endpoint":                f.Origin() + "/authorize",
		"token_endpoint":                        f.Origin() + "/token",
		"registration_endpoint":                 f.Origin() + "/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	}
	if f.Revocation {
		metadata["revocation_endpoint"] = f.Origin() + "/revoke"
	}
	writeJSON(w, 200, metadata)
}

func (f *FakeServer) serveRegister(w http.ResponseWriter, body []byte) {
	var metadata map[string]any
	_ = json.Unmarshal(body, &metadata)
	f.mu.Lock()
	f.clientCounter++
	id := fmt.Sprintf("client-%d", f.clientCounter)
	f.mu.Unlock()
	metadata["client_id"] = id
	metadata["client_id_issued_at"] = 1700000000
	writeJSON(w, 201, metadata)
}

func (f *FakeServer) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "bad authorization request", 400)
		return
	}
	f.mu.Lock()
	code := fmt.Sprintf("code-%d", len(f.codes)+1)
	f.codes[code] = q.Get("redirect_uri")
	f.mu.Unlock()
	values := redirect.Query()
	values.Set("code", code)
	values.Set("state", q.Get("state"))
	values.Set("iss", f.Origin())
	redirect.RawQuery = values.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (f *FakeServer) issueTokens(w http.ResponseWriter) {
	f.mu.Lock()
	f.tokenCounter++
	n := f.tokenCounter
	access := fmt.Sprintf("access-%d", n)
	f.AcceptedTokens[access] = true
	f.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": fmt.Sprintf("refresh-%d", n),
	})
}

func (f *FakeServer) serveToken(w http.ResponseWriter, body []byte) {
	form, _ := url.ParseQuery(string(body))
	switch form.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		_, ok := f.codes[form.Get("code")]
		delete(f.codes, form.Get("code"))
		f.mu.Unlock()
		if !ok || form.Get("code_verifier") == "" {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
			return
		}
		f.issueTokens(w)
	case "refresh_token":
		if f.RefreshStatus != 0 && f.RefreshStatus != 200 {
			writeJSON(w, f.RefreshStatus, map[string]any{"error": "invalid_grant", "error_description": "refresh token revoked"})
			return
		}
		f.issueTokens(w)
	default:
		writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (f *FakeServer) serveWorkOS(w http.ResponseWriter, path string, body []byte) {
	form, _ := url.ParseQuery(string(body))
	switch path {
	case "/user_management/authorize/device":
		writeJSON(w, 200, map[string]any{
			"device_code":               "device-1",
			"user_code":                 "WXYZ-1234",
			"verification_uri":          f.Origin() + "/device",
			"verification_uri_complete": f.Origin() + "/device?user_code=WXYZ-1234",
			"expires_in":                300,
			"interval":                  1,
		})
	case "/user_management/authenticate":
		session := map[string]any{"access_token": "account-access", "refresh_token": "account-refresh", "user": map[string]any{"id": f.Principal}}
		switch form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			f.mu.Lock()
			pending := f.pendingPolls < f.DevicePending
			f.pendingPolls++
			f.mu.Unlock()
			if pending {
				writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
				return
			}
			session["organization_id"] = nil
		case "refresh_token":
			if org := form.Get("organization_id"); org != "" {
				session["organization_id"] = org
			}
		}
		writeJSON(w, 200, session)
	default:
		writeJSON(w, 404, map[string]any{"error": "not_found"})
	}
}

func (f *FakeServer) serveAccount(w http.ResponseWriter, r *http.Request) {
	selected := ""
	if strings.HasSuffix(r.Header.Get("Authorization"), "account-access") {
		selected = "org_wos_1"
	}
	session := map[string]any{
		"mode": "workos", "principalId": f.Principal, "name": "Ada", "email": "ada@example.com",
		"role": "admin", "organizationId": selected, "organizations": f.Organizations,
	}
	switch r.URL.Path {
	case "/api/v1/session":
		writeJSON(w, 200, session)
	case "/api/v1/onboarding":
		writeJSON(w, 200, map[string]any{
			"organizationId": "org_wos_1", "journey": "existing", "next": "/connections", "allowed": true,
			"profile": nil, "step": "complete", "tenantReady": true,
		})
	case "/api/v1/me/bootstrap":
		mcp := map[string]any{"status": f.MCPStatus, "url": f.Resource()}
		if f.MCPStatus == "unconfigured" {
			mcp["url"] = nil
		}
		writeJSON(w, 200, map[string]any{
			"organization": map[string]any{"id": "org_1", "name": "Acme", "environment": "production"},
			"membership":   map[string]any{"principalId": f.Principal, "role": "admin", "active": true, "revocationEpoch": 0},
			"entitlements": map[string]any{"organizationId": "org_1", "package": "standard"},
			"session":      session,
			"mcp":          mcp,
			"settings":     map[string]any{"retention": "metadata_only", "environment": "production"},
		})
	case "/api/v1/mcp-endpoint":
		writeJSON(w, 200, map[string]any{"status": "ready", "url": f.Resource()})
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "missing"}})
	}
}

func (f *FakeServer) serveMCP(w http.ResponseWriter, r *http.Request, body []byte) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	accepted := f.AcceptedTokens[token]
	f.mu.Unlock()
	if !accepted {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.Origin()+`/.well-known/oauth-protected-resource/mcp/u/`+resourceHex+`"`)
		writeJSON(w, 401, map[string]any{"error": "invalid_token"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	}
	var message map[string]any
	if err := json.Unmarshal(body, &message); err != nil {
		writeJSON(w, 400, map[string]any{"error": "parse"})
		return
	}
	method, _ := message["method"].(string)
	id, isRequest := message["id"]
	if !isRequest {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if f.MCPHook != nil && f.MCPHook(method, body) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	reply := map[string]any{"jsonrpc": "2.0", "id": id}
	params, _ := message["params"].(map[string]any)
	switch method {
	case "initialize":
		version, _ := params["protocolVersion"].(string)
		w.Header().Set("Mcp-Session-Id", "session-1")
		reply["result"] = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "fake-durin-gateway", "version": "1.0.0"},
		}
	case "tools/list":
		page := 0
		if cursor, ok := params["cursor"].(string); ok {
			fmt.Sscanf(cursor, "page-%d", &page)
		}
		result := map[string]any{"tools": []Tool{}}
		if page < len(f.Pages) {
			result["tools"] = f.Pages[page]
			if page+1 < len(f.Pages) {
				result["nextCursor"] = fmt.Sprintf("page-%d", page+1)
			}
		}
		reply["result"] = result
	case "tools/call":
		name, _ := params["name"].(string)
		f.mu.Lock()
		queue := f.CallReplies[name]
		var next CallReply
		if len(queue) > 0 {
			next = queue[0]
			f.CallReplies[name] = queue[1:]
		}
		f.mu.Unlock()
		switch {
		case next.Error != nil:
			reply["error"] = next.Error
		case next.Result != nil:
			reply["result"] = next.Result
		default:
			reply["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		}
	case "ping":
		reply["result"] = map[string]any{}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "Method not found"}
	}
	f.writeRPC(w, reply)
}

func (f *FakeServer) writeRPC(w http.ResponseWriter, reply map[string]any) {
	if !f.SSE {
		writeJSON(w, 200, reply)
		return
	}
	var buffer bytes.Buffer
	data, _ := json.Marshal(reply)
	fmt.Fprintf(&buffer, "event: message\nid: evt-1\ndata: %s\n\n", data)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	_, _ = w.Write(buffer.Bytes())
}
