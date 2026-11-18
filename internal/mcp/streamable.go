package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/jsurl"
	"github.com/accessdurin/cli/internal/mcp/oauth"
)

// AuthProvider is the minimal AuthProvider: a bearer token for every request.
type AuthProvider interface {
	Token(ctx context.Context) (string, error)
}

// UnauthorizedHandler is the optional AuthProvider.onUnauthorized hook, run once on a 401
// before the request is retried.
type UnauthorizedHandler interface {
	OnUnauthorized(ctx context.Context, uc oauth.UnauthorizedContext) error
}

// ReconnectionOptions are StreamableHTTPReconnectionOptions.
type ReconnectionOptions struct {
	InitialReconnectionDelay    time.Duration
	MaxReconnectionDelay        time.Duration
	ReconnectionDelayGrowFactor float64
	MaxRetries                  int
}

// DefaultReconnectionOptions are the SDK defaults (1s initial, 30s max, x1.5, 2 retries).
var DefaultReconnectionOptions = ReconnectionOptions{
	InitialReconnectionDelay:    time.Second,
	MaxReconnectionDelay:        30 * time.Second,
	ReconnectionDelayGrowFactor: 1.5,
	MaxRetries:                  2,
}

// StreamableHTTPOptions are StreamableHTTPClientTransportOptions.
type StreamableHTTPOptions struct {
	// AuthProvider supplies bearer tokens. Ignored when OAuthProvider is set.
	AuthProvider AuthProvider
	// OAuthProvider is an OAuthClientProvider; it is adapted to an AuthProvider whose 401
	// handler runs oauth.Auth (adaptOAuthProvider).
	OAuthProvider oauth.ClientProvider
	// Fetch performs every request. Required.
	Fetch httpx.Fetch
	// RequestInit is merged into every request (headers, redirect mode).
	RequestInit *httpx.RequestInit
	// SessionID and ProtocolVersion seed a reconnecting transport.
	SessionID       string
	ProtocolVersion string
	// ReconnectionOptions default to DefaultReconnectionOptions.
	ReconnectionOptions *ReconnectionOptions
	// OnInsufficientScope is "reauthorize" (default) or "throw".
	OnInsufficientScope string
	// MaxStepUpRetries defaults to 1.
	MaxStepUpRetries *int
	// RedirectPolicy is "" (same-origin following via fetchWithinOrigin) or "follow".
	RedirectPolicy               string
	SkipIssuerMetadataValidation bool
	// Warn receives the SDK's console.warn output from auth (401 handling and step-up); nil
	// writes it to os.Stderr, as console.warn does.
	Warn func(string)
}

// StreamableHTTPClientTransport is the SDK's Streamable HTTP client transport.
type StreamableHTTPClientTransport struct {
	handlers handlerBox

	url                 *jsurl.URL
	requestInit         *httpx.RequestInit
	authProvider        AuthProvider
	oauthProvider       oauth.ClientProvider
	fetch               httpx.Fetch
	fetchWithInit       httpx.Fetch
	redirectPolicy      string
	reconnect           ReconnectionOptions
	onInsufficientScope string
	maxStepUpRetries    int
	skipIssuerValidate  bool
	warn                func(string)

	mu                  sync.Mutex
	started             bool
	base                context.Context
	abort               context.CancelFunc
	sessionID           string
	protocolVersion     string
	resourceMetadataURL *jsurl.URL
	scope               string
	serverRetry         *time.Duration
	cancelReconnect     func()
}

// oauthAdapter is adaptOAuthProvider.
type oauthAdapter struct {
	provider oauth.ClientProvider
	opts     oauth.HandleOptions
}

func (a oauthAdapter) Token(ctx context.Context) (string, error) {
	t, err := a.provider.Tokens(ctx, nil)
	if err != nil || t == nil {
		return "", err
	}
	return t.AccessToken, nil
}

func (a oauthAdapter) OnUnauthorized(ctx context.Context, uc oauth.UnauthorizedContext) error {
	return oauth.HandleUnauthorized(ctx, a.provider, uc, a.opts)
}

// NewStreamableHTTPClientTransport creates the transport. It fails when Fetch is nil: the port
// never falls back to a default HTTP client.
func NewStreamableHTTPClientTransport(url *jsurl.URL, opts StreamableHTTPOptions) (*StreamableHTTPClientTransport, error) {
	if opts.Fetch == nil {
		return nil, errors.New("mcp: StreamableHTTPOptions.Fetch is required")
	}
	t := &StreamableHTTPClientTransport{
		url:                 url.Clone(),
		requestInit:         opts.RequestInit,
		fetch:               opts.Fetch,
		redirectPolicy:      opts.RedirectPolicy,
		reconnect:           DefaultReconnectionOptions,
		onInsufficientScope: opts.OnInsufficientScope,
		maxStepUpRetries:    1,
		skipIssuerValidate:  opts.SkipIssuerMetadataValidation,
		warn:                opts.Warn,
		sessionID:           opts.SessionID,
		protocolVersion:     opts.ProtocolVersion,
	}
	if opts.OAuthProvider != nil {
		t.oauthProvider = opts.OAuthProvider
		t.authProvider = oauthAdapter{opts.OAuthProvider, oauth.HandleOptions{SkipIssuerMetadataValidation: opts.SkipIssuerMetadataValidation, Warn: opts.Warn}}
	} else {
		t.authProvider = opts.AuthProvider
	}
	t.fetchWithInit = httpx.WithInit(opts.Fetch, opts.RequestInit)
	if opts.ReconnectionOptions != nil {
		t.reconnect = *opts.ReconnectionOptions
	}
	if t.onInsufficientScope == "" {
		t.onInsufficientScope = "reauthorize"
	}
	if opts.MaxStepUpRetries != nil {
		t.maxStepUpRetries = max(0, *opts.MaxStepUpRetries)
	}
	return t, nil
}

// Handlers implements Transport.
func (t *StreamableHTTPClientTransport) Handlers() Handlers { return t.handlers.get() }

// SetHandlers implements Transport.
func (t *StreamableHTTPClientTransport) SetHandlers(h Handlers) { t.handlers.set(h) }

// SessionID implements Transport.
func (t *StreamableHTTPClientTransport) SessionID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessionID
}

// SetProtocolVersion sets the mcp-protocol-version header value.
func (t *StreamableHTTPClientTransport) SetProtocolVersion(v string) {
	t.mu.Lock()
	t.protocolVersion = v
	t.mu.Unlock()
}

// ProtocolVersion returns the protocol version header value.
func (t *StreamableHTTPClientTransport) ProtocolVersion() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.protocolVersion
}

// Start implements Transport (it only arms the abort controller, as in the SDK).
func (t *StreamableHTTPClientTransport) Start(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return errors.New("StreamableHTTPClientTransport already started! If using Client class, note that connect() calls start() automatically.")
	}
	t.started = true
	t.base, t.abort = context.WithCancel(context.Background())
	return nil
}

// Close implements Transport: cancel any pending reconnection, abort in-flight requests and
// streams, and call onclose.
func (t *StreamableHTTPClientTransport) Close() error {
	t.mu.Lock()
	cancel := t.cancelReconnect
	t.cancelReconnect = nil
	abort := t.abort
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if abort != nil {
		abort()
	}
	t.handlers.close()
	return nil
}

func (t *StreamableHTTPClientTransport) ctx() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.base == nil {
		return context.Background()
	}
	return t.base
}

func (t *StreamableHTTPClientTransport) aborted() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.base != nil && t.base.Err() != nil
}

func (t *StreamableHTTPClientTransport) hasAbortController() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.base != nil
}

func (t *StreamableHTTPClientTransport) redirects() httpx.Fetch {
	if t.redirectPolicy == "follow" {
		return t.fetch
	}
	return httpx.WithinOrigin(t.fetch)
}

func (t *StreamableHTTPClientTransport) unfollowedRedirect(resp *http.Response) string {
	if t.redirectPolicy == "follow" {
		return ""
	}
	text := httpx.UnfollowedRedirect(t.url.Href(), resp)
	if text == "" {
		return ""
	}
	return text + " (redirectPolicy: 'same-origin')"
}

// markAuthSeam returns err unchanged: Go errors.As/Is see through to the provider's error.
// (The SDK stamps a symbol on the error object; only version negotiation reads it.)
func markAuthSeam(err error) error { return err }

func (t *StreamableHTTPClientTransport) commonHeaders(ctx context.Context) (http.Header, error) {
	h := http.Header{}
	if t.requestInit != nil && t.requestInit.Header != nil {
		h = t.requestInit.Header.Clone()
	}
	if t.authProvider != nil {
		token, err := t.authProvider.Token(ctx)
		if err != nil {
			return nil, markAuthSeam(err)
		}
		if token != "" {
			h.Set("Authorization", "Bearer "+token)
		}
	}
	t.mu.Lock()
	sid, pv := t.sessionID, t.protocolVersion
	t.mu.Unlock()
	if sid != "" {
		h.Set("mcp-session-id", sid)
	}
	if pv != "" {
		h.Set("mcp-protocol-version", pv)
	}
	return h, nil
}

func mergeAccept(h http.Header, add ...string) {
	var types []string
	if existing := h.Get("accept"); existing != "" {
		for _, p := range strings.Split(existing, ",") {
			types = append(types, strings.ToLower(strings.TrimSpace(p)))
		}
	}
	types = append(types, add...)
	seen := map[string]bool{}
	var out []string
	for _, x := range types {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	h.Set("accept", strings.Join(out, ", "))
}

func (t *StreamableHTTPClientTransport) init(method string, h http.Header, body []byte) *httpx.RequestInit {
	init := t.requestInit.Clone()
	init.Method = method
	init.Header = h
	init.Body = body
	return init
}

const protocolVersionMetaKey = "io.modelcontextprotocol/protocolVersion"

var mcpNameHeaderSource = map[string]string{
	"tools/call": "name", "prompts/get": "name", "resources/read": "uri",
	"tasks/get": "taskId", "tasks/update": "taskId", "tasks/cancel": "taskId",
}

func envelopeVersion(m Message) (string, bool) {
	if !IsJSONRPCRequest(m) {
		return "", false
	}
	v, ok := jsonv.Path(m, "params", "_meta", protocolVersionMetaKey)
	if !ok {
		return "", false
	}
	str, ok := v.(string)
	return str, ok
}

// applyBodyDerivedHeaders: a request whose _meta carries a protocol-version claim sends
// MCP-Protocol-Version, Mcp-Method and (for named methods) Mcp-Name from the body.
func applyBodyDerivedHeaders(h http.Header, m Message) {
	version, ok := envelopeVersion(m)
	if !ok {
		return
	}
	method, _ := MessageMethod(m)
	h.Set("mcp-protocol-version", version)
	h.Set("mcp-method", method)
	if field, ok := mcpNameHeaderSource[method]; ok {
		if v, ok := jsonv.Path(m, "params", field); ok {
			if str, ok := v.(string); ok {
				h.Set("mcp-name", EncodeMcpParamValue(str))
			}
		}
	}
}

func isModernProtocolVersion(v string) bool { return v >= "2026-07-28" }

var reservedRequestHeaders = map[string]bool{
	"authorization": true, "dpop": true, "content-type": true, "mcp-protocol-version": true,
	"mcp-method": true, "mcp-name": true, "mcp-session-id": true,
}

// Send implements Transport: POST the message and deliver the response (JSON or SSE).
func (t *StreamableHTTPClientTransport) Send(ctx context.Context, message Message, opts *SendOptions) error {
	return t.send(ctx, message, opts, false, 0)
}

func (t *StreamableHTTPClientTransport) send(ctx context.Context, message Message, opts *SendOptions, isAuthRetry bool, stepUpRetries int) (err error) {
	defer func() {
		if err != nil {
			t.handlers.err(err)
		}
	}()
	if opts == nil {
		opts = &SendOptions{}
	}
	if opts.ResumptionToken != "" {
		var replay any
		if IsJSONRPCRequest(message) {
			replay, _ = MessageID(message)
		}
		so := sseOptions{resumptionToken: opts.ResumptionToken, replayMessageID: replay, hasReplay: replay != nil}
		go func() {
			if err := t.startOrAuthSSE(ctx, so, false, 0); err != nil {
				t.handlers.err(err)
			}
		}()
		return nil
	}
	headers, err := t.commonHeaders(ctx)
	if err != nil {
		return err
	}
	applyBodyDerivedHeaders(headers, message)
	isHandshake := IsInitializeRequest(message)
	if isHandshake {
		headers.Del("mcp-session-id")
	}
	for name, value := range opts.Headers {
		if reservedRequestHeaders[strings.ToLower(name)] {
			continue
		}
		headers.Set(name, value)
	}
	headers.Set("content-type", "application/json")
	mergeAccept(headers, "application/json", "text/event-stream")
	body, err := SerializeMessage(message)
	if err != nil {
		return err
	}
	resp, err := t.redirects()(t.ctx(), t.url.Href(), t.init("POST", headers, body))
	if err != nil {
		return err
	}
	if isHandshake && httpx.OK(resp) {
		t.mu.Lock()
		t.sessionID = resp.Header.Get("mcp-session-id")
		t.mu.Unlock()
	}
	if !httpx.OK(resp) {
		if resp.StatusCode == 401 && t.authProvider != nil {
			t.recordChallenge(resp)
			if handler, ok := t.authProvider.(UnauthorizedHandler); ok && !isAuthRetry {
				if err := handler.OnUnauthorized(ctx, oauth.UnauthorizedContext{Response: resp, ServerURL: t.url.Href(), Fetch: t.fetchWithInit}); err != nil {
					httpx.Discard(resp)
					return markAuthSeam(err)
				}
				httpx.Discard(resp)
				return t.send(ctx, message, opts, true, stepUpRetries)
			}
			httpx.Discard(resp)
			if isAuthRetry {
				return markAuthSeam(&SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPAuthentication, Message: "Server returned 401 after re-authentication"}, Status: 401, StatusText: httpx.StatusText(resp)})
			}
			return markAuthSeam(oauth.NewUnauthorizedError())
		}
		var text *string
		if body, err := httpx.Text(resp); err == nil {
			text = &body
		}
		if resp.StatusCode == 403 {
			params := oauth.ExtractWWWAuthenticateParams(resp.Header)
			if params.Error == "insufficient_scope" {
				res, err := t.stepUpAuthorize(ctx, params, httpx.StatusText(resp), text, stepUpRetries)
				if err != nil {
					return err
				}
				if res != oauth.Authorized {
					return markAuthSeam(oauth.NewUnauthorizedError())
				}
				return t.send(ctx, message, opts, isAuthRetry, stepUpRetries+1)
			}
		}
		if resp.StatusCode == 400 && text != nil {
			if version, ok := envelopeVersion(message); ok && isModernProtocolVersion(version) {
				if parsed, err := ParseMessageJSON([]byte(*text)); err == nil && IsJSONRPCErrorResponse(parsed) {
					pid, _ := MessageID(parsed)
					mid, _ := MessageID(message)
					if jsonv.Equal(pid, mid) {
						t.handlers.message(parsed)
						return nil
					}
				}
			}
		}
		reason := t.unfollowedRedirect(resp)
		if reason == "" {
			if text != nil {
				reason = *text
			} else {
				reason = "null"
			}
		}
		return &SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPNotImplemented, Message: "Error POSTing to endpoint: " + reason}, Status: resp.StatusCode, StatusText: httpx.StatusText(resp), Text: text}
	}
	if resp.StatusCode == 202 {
		httpx.Discard(resp)
		if IsInitializedNotification(message) {
			go func() {
				if err := t.startOrAuthSSE(ctx, sseOptions{}, false, 0); err != nil {
					t.handlers.err(err)
				}
			}()
		}
		return nil
	}
	// "method" in msg && "id" in msg && msg.id !== undefined
	_, hasMethod := message.Get("method")
	_, hasID := MessageID(message)
	hasRequests := hasMethod && hasID
	contentType := resp.Header.Get("content-type")
	mediaType := MediaTypeEssence(contentType)
	if !hasRequests {
		httpx.Discard(resp)
		return nil
	}
	switch mediaType {
	case "text/event-stream":
		t.handleSSEStream(resp, sseOptions{onResumptionToken: opts.OnResumptionToken, onRequestStreamEnd: opts.OnRequestStreamEnd}, false)
		return nil
	case "application/json":
		data, err := httpx.JSON(resp)
		if err != nil {
			return err
		}
		var msgs []Message
		if arr, ok := data.([]any); ok {
			for _, item := range arr {
				m, err := ParseMessage(item)
				if err != nil {
					return err
				}
				msgs = append(msgs, m)
			}
		} else {
			m, err := ParseMessage(data)
			if err != nil {
				return err
			}
			msgs = append(msgs, m)
		}
		for _, m := range msgs {
			t.handlers.message(m)
		}
		return nil
	default:
		httpx.Discard(resp)
		shown := contentType
		if resp.Header.Values("content-type") == nil {
			shown = "null"
		}
		return &SdkError{Code: ErrClientHTTPUnexpectedContent, Message: "Unexpected content type: " + shown, Data: map[string]any{"contentType": contentType}}
	}
}

func (t *StreamableHTTPClientTransport) recordChallenge(resp *http.Response) {
	if resp.Header.Values("www-authenticate") == nil {
		return
	}
	params := oauth.ExtractWWWAuthenticateParams(resp.Header)
	t.mu.Lock()
	t.resourceMetadataURL = params.ResourceMetadataURL
	t.scope = oauth.ComputeScopeUnion(t.scope, params.Scope)
	t.mu.Unlock()
}

// stepUpAuthorize is the SEP-2350 step-up for 403 insufficient_scope.
func (t *StreamableHTTPClientTransport) stepUpAuthorize(ctx context.Context, params oauth.WWWAuthenticateParams, statusText string, text *string, stepUpRetries int) (oauth.Result, error) {
	rm := ""
	if params.ResourceMetadataURL != nil {
		rm = params.ResourceMetadataURL.Href()
	}
	if t.onInsufficientScope == "throw" || t.oauthProvider == nil {
		return "", markAuthSeam(&oauth.InsufficientScopeError{RequiredScope: params.Scope, ResourceMetadataURL: rm, ErrorDescription: params.ErrorDescription})
	}
	if stepUpRetries >= t.maxStepUpRetries {
		if statusText == "" {
			statusText = "Forbidden"
		}
		return "", markAuthSeam(&SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPForbidden, Message: fmt.Sprintf("Server returned 403 insufficient_scope after step-up re-authorization (retry limit %d reached)", t.maxStepUpRetries)}, Status: 403, StatusText: statusText, Text: text})
	}
	if params.ResourceMetadataURL != nil {
		t.mu.Lock()
		t.resourceMetadataURL = params.ResourceMetadataURL
		t.mu.Unlock()
	}
	tokens, err := t.oauthProvider.Tokens(ctx, nil)
	if err != nil {
		return "", markAuthSeam(err)
	}
	current := ""
	if tokens != nil && tokens.Scope != nil {
		current = *tokens.Scope
	}
	t.mu.Lock()
	union := oauth.ComputeScopeUnion(t.scope, current, params.Scope)
	t.scope = union
	rmURL := t.resourceMetadataURL
	t.mu.Unlock()
	res, err := oauth.Auth(ctx, t.oauthProvider, oauth.Options{
		ServerURL:                    t.url.Href(),
		ResourceMetadataURL:          rmURL,
		Scope:                        union,
		ForceReauthorization:         oauth.IsStrictScopeSuperset(union, current),
		Fetch:                        t.fetchWithInit,
		SkipIssuerMetadataValidation: t.skipIssuerValidate,
		Warn:                         t.warn,
	})
	if err != nil {
		return "", markAuthSeam(err)
	}
	return res, nil
}

type sseOptions struct {
	resumptionToken    string
	onResumptionToken  func(string)
	replayMessageID    any
	hasReplay          bool
	onRequestStreamEnd func()
}

// startOrAuthSSE opens the standalone GET event stream (or resumes one with Last-Event-ID).
func (t *StreamableHTTPClientTransport) startOrAuthSSE(ctx context.Context, o sseOptions, isAuthRetry bool, stepUpRetries int) (err error) {
	defer func() {
		if err != nil && !t.aborted() {
			t.handlers.err(err)
		}
	}()
	headers, err := t.commonHeaders(ctx)
	if err != nil {
		return err
	}
	mergeAccept(headers, "text/event-stream")
	if o.resumptionToken != "" {
		headers.Set("last-event-id", o.resumptionToken)
	}
	resp, err := t.redirects()(t.ctx(), t.url.Href(), t.init("GET", headers, nil))
	if err != nil {
		return err
	}
	if !httpx.OK(resp) {
		if resp.StatusCode == 401 && t.authProvider != nil {
			t.recordChallenge(resp)
			if handler, ok := t.authProvider.(UnauthorizedHandler); ok && !isAuthRetry {
				if err := handler.OnUnauthorized(ctx, oauth.UnauthorizedContext{Response: resp, ServerURL: t.url.Href(), Fetch: t.fetchWithInit}); err != nil {
					httpx.Discard(resp)
					return markAuthSeam(err)
				}
				httpx.Discard(resp)
				return t.startOrAuthSSE(ctx, o, true, stepUpRetries)
			}
			httpx.Discard(resp)
			if isAuthRetry {
				return markAuthSeam(&SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPAuthentication, Message: "Server returned 401 after re-authentication"}, Status: 401, StatusText: httpx.StatusText(resp)})
			}
			return markAuthSeam(oauth.NewUnauthorizedError())
		}
		if resp.StatusCode == 403 {
			params := oauth.ExtractWWWAuthenticateParams(resp.Header)
			if params.Error == "insufficient_scope" {
				var text *string
				if body, err := httpx.Text(resp); err == nil {
					text = &body
				}
				res, err := t.stepUpAuthorize(ctx, params, httpx.StatusText(resp), text, stepUpRetries)
				if err != nil {
					return err
				}
				if res != oauth.Authorized {
					return markAuthSeam(oauth.NewUnauthorizedError())
				}
				return t.startOrAuthSSE(ctx, o, isAuthRetry, stepUpRetries+1)
			}
		}
		httpx.Discard(resp)
		if resp.StatusCode == 405 {
			if o.onRequestStreamEnd != nil {
				o.onRequestStreamEnd()
			}
			return nil
		}
		reason := t.unfollowedRedirect(resp)
		if reason == "" {
			reason = httpx.StatusText(resp)
		}
		return &SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPFailedToOpenStream, Message: "Failed to open SSE stream: " + reason}, Status: resp.StatusCode, StatusText: httpx.StatusText(resp)}
	}
	t.handleSSEStream(resp, o, true)
	return nil
}

func (t *StreamableHTTPClientTransport) nextReconnectionDelay(attempt int) time.Duration {
	t.mu.Lock()
	retry := t.serverRetry
	t.mu.Unlock()
	if retry != nil {
		return *retry
	}
	d := float64(t.reconnect.InitialReconnectionDelay) * math.Pow(t.reconnect.ReconnectionDelayGrowFactor, float64(attempt))
	return time.Duration(math.Min(d, float64(t.reconnect.MaxReconnectionDelay)))
}

func (t *StreamableHTTPClientTransport) scheduleReconnection(ctx context.Context, o sseOptions, attempt int) {
	if attempt >= t.reconnect.MaxRetries {
		t.handlers.err(fmt.Errorf("Maximum reconnection attempts (%d) exceeded.", t.reconnect.MaxRetries))
		if o.onRequestStreamEnd != nil {
			o.onRequestStreamEnd()
		}
		return
	}
	delay := t.nextReconnectionDelay(attempt)
	timer := time.AfterFunc(delay, func() {
		t.mu.Lock()
		t.cancelReconnect = nil
		t.mu.Unlock()
		if t.aborted() {
			return
		}
		if err := t.startOrAuthSSE(ctx, o, false, 0); err != nil {
			if t.aborted() {
				return
			}
			t.handlers.err(fmt.Errorf("Failed to reconnect SSE stream: %s", err.Error()))
			t.scheduleReconnection(ctx, o, attempt+1)
		}
	})
	t.mu.Lock()
	t.cancelReconnect = func() { timer.Stop() }
	t.mu.Unlock()
}

// handleSSEStream reads an SSE body in a goroutine, delivering messages and scheduling
// reconnection as the SDK does.
func (t *StreamableHTTPClientTransport) handleSSEStream(resp *http.Response, o sseOptions, reconnectable bool) {
	if resp.Body == nil {
		if o.onRequestStreamEnd != nil {
			o.onRequestStreamEnd()
		}
		return
	}
	go func() {
		defer resp.Body.Close()
		var lastEventID string
		hasPriming, receivedResponse := false, false
		readErr := parseSSE(resp.Body, func(ev sseEvent) {
			if ev.ID != "" {
				lastEventID = ev.ID
				hasPriming = true
				if o.onResumptionToken != nil {
					o.onResumptionToken(ev.ID)
				}
			}
			if ev.Data == "" {
				return
			}
			if ev.Event != "" && ev.Event != "message" {
				return
			}
			v, err := jsonv.ParseString(ev.Data)
			if err != nil {
				t.handlers.err(err)
				return
			}
			m, err := ParseMessage(v)
			if err != nil {
				t.handlers.err(err)
				return
			}
			if IsJSONRPCResultResponse(m) || IsJSONRPCErrorResponse(m) {
				receivedResponse = true
				if o.hasReplay {
					m.Set("id", o.replayMessageID)
				}
			}
			t.handlers.message(m)
		}, func(ms int) {
			d := time.Duration(ms) * time.Millisecond
			t.mu.Lock()
			t.serverRetry = &d
			t.mu.Unlock()
		})
		next := sseOptions{resumptionToken: lastEventID, onResumptionToken: o.onResumptionToken, replayMessageID: o.replayMessageID, hasReplay: o.hasReplay, onRequestStreamEnd: o.onRequestStreamEnd}
		if readErr != nil {
			if t.aborted() {
				return
			}
			t.handlers.err(fmt.Errorf("SSE stream disconnected: %s", jsErrorString(readErr)))
		}
		if (reconnectable || hasPriming) && !receivedResponse && t.hasAbortController() && !t.aborted() {
			t.scheduleReconnection(context.Background(), next, 0)
		} else if !t.aborted() && o.onRequestStreamEnd != nil {
			o.onRequestStreamEnd()
		}
	}()
}

// TerminateSession sends DELETE with the session id (405 is tolerated) and forgets the id.
func (t *StreamableHTTPClientTransport) TerminateSession(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			t.handlers.err(err)
		}
	}()
	if t.SessionID() == "" {
		return nil
	}
	headers, err := t.commonHeaders(ctx)
	if err != nil {
		return err
	}
	resp, err := t.redirects()(t.ctx(), t.url.Href(), t.init("DELETE", headers, nil))
	if err != nil {
		return err
	}
	httpx.Discard(resp)
	if !httpx.OK(resp) && resp.StatusCode != 405 {
		reason := t.unfollowedRedirect(resp)
		if reason == "" {
			reason = httpx.StatusText(resp)
		}
		return &SdkHTTPError{SdkError: SdkError{Code: ErrClientHTTPFailedToTerminateSessn, Message: "Failed to terminate session: " + reason}, Status: resp.StatusCode, StatusText: httpx.StatusText(resp)}
	}
	t.mu.Lock()
	t.sessionID = ""
	t.mu.Unlock()
	return nil
}

// ResumeStream opens a GET SSE stream with Last-Event-ID.
func (t *StreamableHTTPClientTransport) ResumeStream(ctx context.Context, lastEventID string, onResumptionToken func(string)) error {
	return t.startOrAuthSSE(ctx, sseOptions{resumptionToken: lastEventID, onResumptionToken: onResumptionToken}, false, 0)
}
