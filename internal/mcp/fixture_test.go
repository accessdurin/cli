package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/jsurl"
	"github.com/accessdurin/cli/internal/mcp/oauth"
)

// The scenarios in testdata/scenarios were run against the real TypeScript SDK by
// testdata/record.mjs, which wrote testdata/fixtures. These tests run the same scenarios
// against the port and require the same HTTP request sequence (method, URL, headers, body,
// redirect mode), the same provider calls and the same results.

type scenario struct {
	Provider map[string]json.RawMessage `json:"provider"`
	Client   *struct {
		Name         string `json:"name"`
		Version      string `json:"version"`
		ListMaxPages *int   `json:"listMaxPages"`
	} `json:"client"`
	Transport *struct {
		URL                 string                     `json:"url"`
		RequestInit         *struct{ Redirect string } `json:"requestInit"`
		OnInsufficientScope string                     `json:"onInsufficientScope"`
	} `json:"transport"`
	Routes []struct {
		Method    string `json:"method"`
		Host      string `json:"host"`
		Path      string `json:"path"`
		RPCMethod string `json:"rpcMethod"`
		Responses []struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			JSON    json.RawMessage   `json:"json"`
			SSE     []string          `json:"sse"`
			Text    *string           `json:"text"`
		} `json:"responses"`
	} `json:"routes"`
	Steps []struct {
		Op                string          `json:"op"`
		ServerURL         string          `json:"serverUrl"`
		AuthorizationCode *string         `json:"authorizationCode"`
		Iss               *string         `json:"iss"`
		URL               string          `json:"url"`
		Params            json.RawMessage `json:"params"`
		Message           json.RawMessage `json:"message"`
	} `json:"steps"`
}

type recordedRequest struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Body     *string           `json:"body"`
	Redirect *string           `json:"redirect"`
}

type fixtureStep struct {
	Op       string            `json:"op"`
	Result   json.RawMessage   `json:"result"`
	Error    *errorRecord      `json:"error"`
	Requests []recordedRequest `json:"requests"`
	Calls    []json.RawMessage `json:"calls"`
	Warnings []string          `json:"warnings"`
}

type errorRecord struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Code    any    `json:"code,omitempty"`
}

type fixture struct {
	Steps           []fixtureStep `json:"steps"`
	TransportErrors []errorRecord `json:"transportErrors"`
}

var (
	challengeRe = regexp.MustCompile(`code_challenge=[A-Za-z0-9_-]{43}`)
	verifierRe  = regexp.MustCompile(`code_verifier=(?:[A-Za-z0-9._-]|%7E){43}`)
)

func normalize(s string) string {
	s = challengeRe.ReplaceAllString(s, "code_challenge=<challenge>")
	return verifierRe.ReplaceAllString(s, "code_verifier=<verifier>")
}

// canonical sorts object keys recursively, like the recorder's canonical().
func canonical(v any) any {
	switch t := v.(type) {
	case *jsonv.Object:
		keys := t.Keys()
		sort.Strings(keys)
		out := jsonv.NewObject()
		for _, k := range keys {
			x, _ := t.Get(k)
			if jsonv.IsUndefined(x) {
				continue
			}
			out.Set(k, canonical(x))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = canonical(x)
		}
		return out
	}
	return v
}

func toJSValue(t *testing.T, v any) any {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out, err := jsonv.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type harness struct {
	t        *testing.T
	sc       *scenario
	mu       sync.Mutex
	requests []recordedRequest
	calls    []string
	counters map[int]int
	warnings []string
}

func (h *harness) warn(msg string) {
	h.mu.Lock()
	h.warnings = append(h.warnings, msg)
	h.mu.Unlock()
}

func (h *harness) fetch(ctx context.Context, url string, init *httpx.RequestInit) (*http.Response, error) {
	u, err := jsurl.Parse(url, nil)
	if err != nil {
		return nil, &httpx.TypeError{Err: err}
	}
	method := init.Method
	if method == "" {
		method = "GET"
	}
	headers := map[string]string{}
	for k, vs := range init.Header {
		headers[strings.ToLower(k)] = normalize(strings.Join(vs, ", "))
	}
	rec := recordedRequest{Method: method, URL: normalize(u.Href()), Headers: headers}
	if init.Body != nil {
		b := normalize(string(init.Body))
		rec.Body = &b
	}
	if init.Redirect != "" {
		r := string(init.Redirect)
		rec.Redirect = &r
	}
	var rpcMethod string
	var rpcID any
	if init.Body != nil {
		if v, err := jsonv.Parse(init.Body); err == nil {
			if o, ok := v.(*jsonv.Object); ok {
				if m, ok := o.Get("method"); ok {
					rpcMethod, _ = m.(string)
				}
				rpcID, _ = o.Get("id")
			}
		}
	}
	h.mu.Lock()
	h.requests = append(h.requests, rec)
	idx := -1
	for i, r := range h.sc.Routes {
		if r.Method == method && r.Path == u.Pathname() && (r.Host == "" || r.Host == u.Hostname()) && (r.RPCMethod == "" || r.RPCMethod == rpcMethod) {
			idx = i
			break
		}
	}
	if idx == -1 {
		h.mu.Unlock()
		return &http.Response{StatusCode: 599, Status: "599 ", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no route"))}, nil
	}
	route := h.sc.Routes[idx]
	n := h.counters[idx]
	h.counters[idx] = n + 1
	h.mu.Unlock()
	spec := route.Responses[min(n, len(route.Responses)-1)]
	idJSON := jsonv.MustStringify(rpcID)
	sub := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, `"$id"`, idJSON), "$id", idJSON)
	}
	hdr := http.Header{}
	for k, v := range spec.Headers {
		hdr.Set(k, v)
	}
	var body string
	switch {
	case spec.JSON != nil:
		v, err := jsonv.Parse(spec.JSON)
		if err != nil {
			h.t.Fatal(err)
		}
		body = sub(jsonv.MustStringify(v))
		if hdr.Get("content-type") == "" {
			hdr.Set("content-type", "application/json")
		}
	case spec.SSE != nil:
		for _, e := range spec.SSE {
			body += sub(e)
		}
		if hdr.Get("content-type") == "" {
			hdr.Set("content-type", "text/event-stream")
		}
	case spec.Text != nil:
		body = *spec.Text
		if hdr.Get("content-type") == "" {
			hdr.Set("content-type", "text/plain;charset=UTF-8")
		}
	}
	return &http.Response{StatusCode: spec.Status, Status: http.StatusText(spec.Status), Header: hdr, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// scriptedProvider mirrors makeProvider() in record.mjs.
type scriptedProvider struct {
	h           *harness
	redirectURL string
	state       string
	stateThrows string
	metadata    *jsonv.Object
	tokens      *oauth.Tokens
	client      *oauth.ClientInformation
	discovery   *oauth.DiscoveryState
	verifier    string
}

func (p *scriptedProvider) log(name string, args ...any) {
	t := p.h.t
	parts := []any{name}
	for _, a := range args {
		parts = append(parts, canonical(a))
	}
	p.h.mu.Lock()
	p.h.calls = append(p.h.calls, jsonv.MustStringify(parts))
	p.h.mu.Unlock()
	_ = t
}

func ctxValue(info *oauth.ClientInformationContext) any {
	if info == nil {
		return nil
	}
	return jsonv.NewObject("issuer", info.Issuer)
}

func (p *scriptedProvider) RedirectURL() string           { return p.redirectURL }
func (p *scriptedProvider) ClientMetadata() *jsonv.Object { return p.metadata }
func (p *scriptedProvider) State(context.Context) (string, error) {
	p.log("state")
	if p.stateThrows != "" {
		return "", errors.New(p.stateThrows)
	}
	return p.state, nil
}
func (p *scriptedProvider) RedirectToAuthorization(_ context.Context, u *jsurl.URL) error {
	p.log("redirectToAuthorization", normalize(u.Href()))
	sp := u.SearchParams()
	challenge, _ := sp.Get("code_challenge")
	if challenge != oauth.PKCEChallenge(p.verifier) || len(p.verifier) != 43 {
		p.h.t.Errorf("code_challenge %q does not match the saved verifier", challenge)
	}
	return nil
}
func (p *scriptedProvider) SaveCodeVerifier(_ context.Context, v string) error {
	p.log("saveCodeVerifier", "<verifier>")
	p.verifier = v
	return nil
}
func (p *scriptedProvider) CodeVerifier(context.Context) (string, error) {
	p.log("codeVerifier")
	if p.verifier == "" {
		return "stored-verifier", nil
	}
	return p.verifier, nil
}
func (p *scriptedProvider) ClientInformation(_ context.Context, info *oauth.ClientInformationContext) (*oauth.ClientInformation, error) {
	p.log("clientInformation", ctxValue(info))
	return p.client, nil
}
func (p *scriptedProvider) SaveClientInformation(_ context.Context, c oauth.ClientInformation, info *oauth.ClientInformationContext) error {
	p.log("saveClientInformation", toJSValue(p.h.t, c), ctxValue(info))
	p.client = &c
	return nil
}
func (p *scriptedProvider) Tokens(_ context.Context, info *oauth.ClientInformationContext) (*oauth.Tokens, error) {
	p.log("tokens", ctxValue(info))
	return p.tokens, nil
}
func (p *scriptedProvider) SaveTokens(_ context.Context, tok oauth.Tokens, info *oauth.ClientInformationContext) error {
	p.log("saveTokens", toJSValue(p.h.t, tok), ctxValue(info))
	p.tokens = &tok
	return nil
}
func (p *scriptedProvider) ValidateResourceURL(_ context.Context, serverURL *jsurl.URL, resource *string) (*jsurl.URL, error) {
	var r any
	if resource != nil {
		r = *resource
	}
	p.log("validateResourceURL", serverURL.Href(), r)
	return jsurl.Parse(serverURL.Href(), nil)
}
func discoveryValue(s oauth.DiscoveryState) any {
	o := jsonv.NewObject("authorizationServerUrl", s.AuthorizationServerURL)
	if s.ResourceMetadataURL != nil {
		o.Set("resourceMetadataUrl", *s.ResourceMetadataURL)
	}
	if s.ResourceMetadata != nil {
		o.Set("resourceMetadata", s.ResourceMetadata.Raw)
	}
	if s.AuthorizationServerMetadata != nil {
		o.Set("authorizationServerMetadata", s.AuthorizationServerMetadata.Raw)
	}
	return o
}
func (p *scriptedProvider) SaveDiscoveryState(_ context.Context, s oauth.DiscoveryState) error {
	p.log("saveDiscoveryState", discoveryValue(s))
	p.discovery = &s
	return nil
}
func (p *scriptedProvider) DiscoveryState(context.Context) (*oauth.DiscoveryState, error) {
	p.log("discoveryState")
	return p.discovery, nil
}
func (p *scriptedProvider) InvalidateCredentials(_ context.Context, scope oauth.InvalidationScope) error {
	p.log("invalidateCredentials", string(scope))
	switch scope {
	case oauth.InvalidateAll:
		p.tokens, p.client, p.verifier, p.discovery = nil, nil, "", nil
	case oauth.InvalidateTokens:
		p.tokens = nil
	case oauth.InvalidateClient:
		p.client = nil
	case oauth.InvalidateVerifier:
		p.verifier = ""
	case oauth.InvalidateDiscovery:
		p.discovery = nil
	}
	return nil
}

func newScriptedProvider(t *testing.T, h *harness, raw map[string]json.RawMessage) *scriptedProvider {
	p := &scriptedProvider{h: h}
	str := func(key string) string {
		var s *string
		if v, ok := raw[key]; ok {
			_ = json.Unmarshal(v, &s)
		}
		if s == nil {
			return ""
		}
		return *s
	}
	p.redirectURL, p.state, p.stateThrows = str("redirectUrl"), str("state"), str("stateThrows")
	if v, ok := raw["clientMetadata"]; ok {
		o, err := jsonv.Parse(v)
		if err != nil {
			t.Fatal(err)
		}
		p.metadata = o.(*jsonv.Object)
	}
	if v, ok := raw["tokens"]; ok {
		tok, err := oauth.ParseTokensJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		p.tokens = tok
	}
	if v, ok := raw["clientInformation"]; ok {
		o, _ := jsonv.Parse(v)
		c, err := oauth.ParseClientInformation(o)
		if err != nil {
			t.Fatal(err)
		}
		p.client = c
	}
	return p
}

func goErrorRecord(err error) errorRecord {
	var (
		oe  *oauth.OAuthError
		ue  *oauth.UnauthorizedError
		ime *oauth.IssuerMismatchError
		ise *oauth.InsufficientScopeError
		he  *SdkHTTPError
		se  *SdkError
		pe  *ProtocolError
	)
	switch {
	case errors.As(err, &he):
		return errorRecord{Name: "SdkHttpError", Message: he.Message, Code: string(he.Code)}
	case errors.As(err, &se):
		return errorRecord{Name: "SdkError", Message: se.Message, Code: string(se.Code)}
	case errors.As(err, &pe):
		return errorRecord{Name: "ProtocolError", Message: pe.Message, Code: float64(pe.Code)}
	case errors.As(err, &oe):
		return errorRecord{Name: "OAuthError", Message: oe.Message, Code: oe.Code}
	case errors.As(err, &ue):
		return errorRecord{Name: "UnauthorizedError", Message: ue.Error()}
	case errors.As(err, &ime):
		return errorRecord{Name: "IssuerMismatchError", Message: ime.Error()}
	case errors.As(err, &ise):
		return errorRecord{Name: "InsufficientScopeError", Message: ise.Error()}
	}
	return errorRecord{Name: "Error", Message: err.Error()}
}

// zodMessage reports whether a message embeds a zod issue dump (whose formatting differs).
func zodMessage(m string) bool { return strings.Contains(m, ": [\n") || strings.Contains(m, ": [") }

func compareErrors(t *testing.T, where string, got, want errorRecord) {
	t.Helper()
	gotCode, _ := json.Marshal(got.Code)
	wantCode, _ := json.Marshal(want.Code)
	if got.Name != want.Name || string(gotCode) != string(wantCode) {
		t.Errorf("%s: error %s/%s %q, SDK %s/%s %q", where, got.Name, gotCode, got.Message, want.Name, wantCode, want.Message)
		return
	}
	if zodMessage(want.Message) {
		prefix := want.Message[:strings.Index(want.Message, ": [")]
		if !strings.HasPrefix(got.Message, prefix) {
			t.Errorf("%s: error message %q, SDK %q", where, got.Message, want.Message)
		}
		return
	}
	if got.Message != want.Message {
		t.Errorf("%s: error message\n got %q\n SDK %q", where, got.Message, want.Message)
	}
}

func TestSDKFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/scenarios/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no scenarios: %v", err)
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var sc scenario
			if err := json.Unmarshal(raw, &sc); err != nil {
				t.Fatal(err)
			}
			fxRaw, err := os.ReadFile(filepath.Join("testdata/fixtures", name))
			if err != nil {
				t.Fatal(err)
			}
			var fx fixture
			if err := json.Unmarshal(fxRaw, &fx); err != nil {
				t.Fatal(err)
			}
			runScenario(t, &sc, &fx)
		})
	}
}

func runScenario(t *testing.T, sc *scenario, fx *fixture) {
	h := &harness{t: t, sc: sc, counters: map[int]int{}}
	provider := newScriptedProvider(t, h, sc.Provider)
	ctx := context.Background()
	var (
		client          *Client
		transport       *StreamableHTTPClientTransport
		transportErrors []errorRecord
		teMu            sync.Mutex
	)
	ensureTransport := func() *StreamableHTTPClientTransport {
		if transport != nil {
			return transport
		}
		u := jsurl.MustParse(sc.Transport.URL)
		opts := StreamableHTTPOptions{OAuthProvider: provider, Fetch: h.fetch, OnInsufficientScope: sc.Transport.OnInsufficientScope, Warn: h.warn}
		if sc.Transport.RequestInit != nil {
			opts.RequestInit = &httpx.RequestInit{Redirect: httpx.RedirectMode(sc.Transport.RequestInit.Redirect)}
		}
		var err error
		transport, err = NewStreamableHTTPClientTransport(u, opts)
		if err != nil {
			t.Fatal(err)
		}
		return transport
	}
	if len(fx.Steps) != len(sc.Steps) {
		t.Fatalf("fixture has %d steps, scenario %d", len(fx.Steps), len(sc.Steps))
	}
	for i, step := range sc.Steps {
		h.mu.Lock()
		reqAt, callsAt := len(h.requests), len(h.calls)
		h.warnings = nil
		h.mu.Unlock()
		var result any
		var stepErr error
		switch step.Op {
		case "auth":
			var res oauth.Result
			res, stepErr = oauth.Auth(ctx, provider, oauth.Options{ServerURL: step.ServerURL, Fetch: h.fetch, AuthorizationCode: step.AuthorizationCode, Iss: step.Iss, Warn: h.warn})
			result = string(res)
		case "discoverAS":
			var meta *oauth.AuthorizationServerMetadata
			meta, stepErr = oauth.DiscoverAuthorizationServerMetadata(ctx, step.URL, oauth.AuthorizationServerDiscoveryOptions{Fetch: h.fetch})
			if meta != nil {
				result = jsonv.MustStringify(meta.Raw)
			}
		case "discoverPRM":
			var meta *oauth.ProtectedResourceMetadata
			meta, stepErr = oauth.DiscoverOAuthProtectedResourceMetadata(ctx, step.URL, nil, h.fetch)
			if meta != nil {
				result = jsonv.MustStringify(meta.Raw)
			}
		case "connect":
			opts := ClientOptions{}
			if sc.Client.ListMaxPages != nil {
				opts.ListMaxPages = *sc.Client.ListMaxPages
			}
			client = NewClient(Implementation{Name: sc.Client.Name, Version: sc.Client.Version}, opts)
			tr := ensureTransport()
			stepErr = client.Connect(ctx, tr, RequestOptions{Timeout: 15 * time.Second})
			var sid, pv any
			if s := tr.SessionID(); s != "" {
				sid = s
			}
			if p := tr.ProtocolVersion(); p != "" {
				pv = p
			}
			result = map[string]any{"protocolVersion": pv, "sessionId": sid}
		case "listTools":
			var out *jsonv.Object
			out, stepErr = client.ListTools(ctx, nil, RequestOptions{Timeout: 15 * time.Second})
			if out != nil {
				result = jsonv.MustStringify(out)
			}
		case "callTool":
			v, err := jsonv.Parse(step.Params)
			if err != nil {
				t.Fatal(err)
			}
			var out *jsonv.Object
			out, stepErr = client.CallTool(ctx, CallToolParams{Extra: v.(*jsonv.Object)}, RequestOptions{Timeout: 60 * time.Second})
			if out != nil {
				result = jsonv.MustStringify(out)
			}
		case "send":
			tr := ensureTransport()
			var received []any
			var recvMu sync.Mutex
			tr.SetHandlers(Handlers{
				OnError: func(err error) {
					teMu.Lock()
					transportErrors = append(transportErrors, goErrorRecord(err))
					teMu.Unlock()
				},
				OnMessage: func(m Message) {
					recvMu.Lock()
					received = append(received, jsonv.MustStringify(m))
					recvMu.Unlock()
				},
			})
			if err := tr.Start(ctx); err != nil {
				t.Fatal(err)
			}
			m, err := ParseMessageJSON(step.Message)
			if err != nil {
				t.Fatal(err)
			}
			stepErr = tr.Send(ctx, m, nil)
			if received == nil {
				received = []any{}
			}
			result = received
		case "close":
			stepErr = client.Close()
		default:
			t.Fatalf("unknown op %s", step.Op)
		}
		time.Sleep(50 * time.Millisecond)
		want := fx.Steps[i]
		where := step.Op + "#" + string(rune('0'+i))
		if want.Error != nil {
			if stepErr == nil {
				t.Errorf("%s: got result %v, SDK error %+v", where, result, *want.Error)
			} else {
				compareErrors(t, where, goErrorRecord(stepErr), *want.Error)
			}
		} else if stepErr != nil {
			t.Errorf("%s: unexpected error %v (SDK result %s)", where, stepErr, want.Result)
		} else {
			got, _ := json.Marshal(result)
			if !jsonEqual(got, want.Result) {
				t.Errorf("%s: result\n got %s\n SDK %s", where, got, want.Result)
			}
		}
		h.mu.Lock()
		gotReqs := append([]recordedRequest(nil), h.requests[reqAt:]...)
		gotCalls := append([]string(nil), h.calls[callsAt:]...)
		gotWarnings := append([]string{}, h.warnings...)
		h.mu.Unlock()
		if want.Warnings == nil {
			want.Warnings = []string{}
		}
		if strings.Join(gotWarnings, "\n") != strings.Join(want.Warnings, "\n") {
			t.Errorf("%s: warnings\n got %q\n SDK %q", where, gotWarnings, want.Warnings)
		}
		compareRequests(t, where, gotReqs, want.Requests)
		if len(gotCalls) != len(want.Calls) {
			t.Errorf("%s: %d provider calls, SDK %d\n got  %s\n SDK  %s", where, len(gotCalls), len(want.Calls), strings.Join(gotCalls, "\n      "), joinRaw(want.Calls))
		} else {
			for k := range gotCalls {
				if !jsonEqual([]byte(gotCalls[k]), want.Calls[k]) {
					t.Errorf("%s: provider call %d\n got %s\n SDK %s", where, k, gotCalls[k], want.Calls[k])
				}
			}
		}
	}
	if len(fx.TransportErrors) != len(transportErrors) {
		t.Errorf("transport errors %+v, SDK %+v", transportErrors, fx.TransportErrors)
	} else {
		for i := range transportErrors {
			compareErrors(t, "transport error", transportErrors[i], fx.TransportErrors[i])
		}
	}
}

func joinRaw(list []json.RawMessage) string {
	parts := make([]string, len(list))
	for i, r := range list {
		parts[i] = string(r)
	}
	return strings.Join(parts, "\n      ")
}

func jsonEqual(a, b []byte) bool {
	va, err1 := jsonv.Parse(a)
	vb, err2 := jsonv.Parse(b)
	if err1 != nil || err2 != nil {
		return bytes.Equal(a, b)
	}
	return jsonv.Equal(canonical(va), canonical(vb))
}

func compareRequests(t *testing.T, where string, got, want []recordedRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d requests, SDK %d", where, len(got), len(want))
		for _, r := range got {
			t.Logf("  got %s %s", r.Method, r.URL)
		}
		for _, r := range want {
			t.Logf("  SDK %s %s", r.Method, r.URL)
		}
		return
	}
	for i := range got {
		g, w := got[i], want[i]
		gj, _ := json.Marshal(g)
		wj, _ := json.Marshal(w)
		if string(gj) != string(wj) {
			t.Errorf("%s: request %d\n got %s\n SDK %s", where, i, gj, wj)
		}
	}
}
