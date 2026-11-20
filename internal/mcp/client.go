package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/accessdurin/cli/internal/jsjson"
	s "github.com/accessdurin/cli/internal/mcp/schema"
)

// Protocol versions (core src/constants.ts).
const (
	LatestProtocolVersion            = "2025-11-25"
	DefaultNegotiatedProtocolVersion = "2025-03-26"
	DefaultRequestTimeout            = 60 * time.Second
	DefaultListMaxPages              = 64
)

// SupportedProtocolVersions is SUPPORTED_PROTOCOL_VERSIONS, in preference order. The client
// offers the first entry in initialize and accepts any entry the server answers with.
var SupportedProtocolVersions = []string{LatestProtocolVersion, "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

// Implementation is the clientInfo object (serialized as name, version, then the optional
// fields that are set).
type Implementation struct {
	Name        string
	Version     string
	Title       string
	WebsiteURL  string
	Description string
}

func (i Implementation) object() *jsjson.Object {
	o := jsjson.Obj(jsjson.KV("name", jsjson.String(i.Name)), jsjson.KV("version", jsjson.String(i.Version)))
	if i.Title != "" {
		o.Set("title", jsjson.String(i.Title))
	}
	if i.WebsiteURL != "" {
		o.Set("websiteUrl", jsjson.String(i.WebsiteURL))
	}
	if i.Description != "" {
		o.Set("description", jsjson.String(i.Description))
	}
	return o
}

// ClientOptions are the ClientOptions the port supports.
type ClientOptions struct {
	// Capabilities are the client capabilities sent in initialize (default {}).
	Capabilities *jsjson.Object
	// ListMaxPages caps the auto-aggregating listTools walk: 0 means the default (64); a
	// negative value disables the cap (TypeScript listMaxPages: 0).
	ListMaxPages int
	// EnforceStrictCapabilities makes requests fail when the server lacks the capability.
	EnforceStrictCapabilities bool
	// SupportedProtocolVersions defaults to SupportedProtocolVersions.
	SupportedProtocolVersions []string
	// JSONSchemaValidator compiles outputSchema; nil uses BasicJSONSchemaValidator.
	JSONSchemaValidator JSONSchemaValidator
	// DefaultCacheTTL is defaultCacheTtlMs for list results without ttlMs (default 0: stored,
	// never served).
	DefaultCacheTTL time.Duration
}

// RequestOptions are the per-request options the port supports.
type RequestOptions struct {
	// Timeout defaults to DefaultRequestTimeout.
	Timeout time.Duration
	// OnProgress subscribes to progress notifications (adds _meta.progressToken).
	OnProgress func(params *jsjson.Object)
	// ResetTimeoutOnProgress restarts the timeout on each progress notification.
	ResetTimeoutOnProgress bool
	// MaxTotalTimeout bounds the total time when the timeout is reset by progress.
	MaxTotalTimeout time.Duration
}

type responseHandler func(resp Message, err error)

type pendingTimeout struct {
	timer      *time.Timer
	start      time.Time
	timeout    time.Duration
	maxTotal   time.Duration
	resetOnPrg bool
	onTimeout  func()
}

// Client is the SDK's Client (legacy-era subset: the 'legacy' version negotiation mode, which is
// the SDK default and what the CLI uses; server/discover negotiation is not ported).
type Client struct {
	info      Implementation
	caps      *jsjson.Object
	opts      ClientOptions
	validator JSONSchemaValidator

	mu                 sync.Mutex
	transport          Transport
	nextID             int64
	responseHandlers   map[float64]responseHandler
	progressHandlers   map[float64]func(*jsjson.Object)
	timeouts           map[float64]*pendingTimeout
	serverCapabilities *jsjson.Object
	serverVersion      *jsjson.Object
	instructions       *string
	negotiated         string
	cache              toolsCache

	// OnError and OnClose are the client's onerror / onclose; set them before Connect.
	OnError func(error)
	OnClose func()
}

// NewClient creates a client.
func NewClient(info Implementation, opts ClientOptions) *Client {
	caps := jsjson.NewObject()
	if opts.Capabilities != nil {
		caps = opts.Capabilities.Clone()
	}
	v := opts.JSONSchemaValidator
	if v == nil {
		v = BasicJSONSchemaValidator{}
	}
	if opts.SupportedProtocolVersions == nil {
		opts.SupportedProtocolVersions = SupportedProtocolVersions
	}
	return &Client{
		info: info, caps: caps, opts: opts, validator: v,
		responseHandlers: map[float64]responseHandler{},
		progressHandlers: map[float64]func(*jsjson.Object){},
		timeouts:         map[float64]*pendingTimeout{},
	}
}

func (c *Client) onerror(err error) {
	c.mu.Lock()
	f := c.OnError
	c.mu.Unlock()
	if f != nil {
		f(err)
	}
}

// ServerCapabilities returns the server's capabilities after initialization.
func (c *Client) ServerCapabilities() *jsjson.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverCapabilities
}

// ServerVersion returns the server's serverInfo after initialization.
func (c *Client) ServerVersion() *jsjson.Object {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serverVersion
}

// Instructions returns the server's instructions, if any.
func (c *Client) Instructions() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.instructions == nil {
		return "", false
	}
	return *c.instructions, true
}

// NegotiatedProtocolVersion returns the protocol version agreed in initialize.
func (c *Client) NegotiatedProtocolVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.negotiated
}

// Transport returns the connected transport, or nil.
func (c *Client) Transport() Transport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport
}

func (c *Client) resetConnectionState() {
	c.mu.Lock()
	c.negotiated = ""
	c.serverCapabilities = nil
	c.serverVersion = nil
	c.instructions = nil
	c.cache.reset()
	c.mu.Unlock()
}

// Connect attaches the transport (chaining handlers already set on it), starts it and runs the
// initialize handshake: initialize with the first supported legacy version, check the answer is
// a supported version, set the transport's protocol version, then notifications/initialized.
// A transport that already has a session id skips the handshake. On a handshake failure the
// client is closed and the error returned.
func (c *Client) Connect(ctx context.Context, transport Transport, opts RequestOptions) error {
	c.mu.Lock()
	c.transport = transport
	c.mu.Unlock()
	prev := transport.Handlers()
	transport.SetHandlers(Handlers{
		OnClose: func() {
			defer c.onclose()
			if prev.OnClose != nil {
				prev.OnClose()
			}
		},
		OnError: func(err error) {
			if prev.OnError != nil {
				prev.OnError(err)
			}
			c.onerror(err)
		},
		OnMessage: func(m Message) {
			if prev.OnMessage != nil {
				prev.OnMessage(m)
			}
			c.dispatch(m)
		},
	})
	if err := transport.Start(ctx); err != nil {
		return err
	}
	if transport.SessionID() != "" {
		if pv := c.NegotiatedProtocolVersion(); pv != "" {
			if setter, ok := transport.(ProtocolVersionSetter); ok {
				setter.SetProtocolVersion(pv)
			}
		}
		return nil
	}
	c.resetConnectionState()
	if err := c.legacyHandshake(ctx, transport, opts); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

func (c *Client) legacyVersions() []string {
	var out []string
	for _, v := range c.opts.SupportedProtocolVersions {
		if !isModernProtocolVersion(v) {
			out = append(out, v)
		}
	}
	return out
}

func (c *Client) legacyHandshake(ctx context.Context, transport Transport, opts RequestOptions) error {
	versions := c.legacyVersions()
	if len(versions) == 0 {
		return &SdkError{Code: ErrEraNegotiationFailed, Message: "Cannot run the initialize handshake: supportedProtocolVersions contains no pre-2026-07-28 protocol version"}
	}
	params := jsjson.Obj(
		jsjson.KV("protocolVersion", jsjson.String(versions[0])),
		jsjson.KV("capabilities", c.caps),
		jsjson.KV("clientInfo", c.info.object()),
	)
	out, err := c.request(ctx, "initialize", params, InitializeResultWireSchema, opts)
	if err != nil {
		return err
	}
	result := out.(*jsjson.Object)
	pvs, _ := result.GetString("protocolVersion")
	supported := false
	for _, v := range versions {
		if v == pvs {
			supported = true
		}
	}
	if !supported {
		return fmt.Errorf("Server's protocol version is not supported: %s", pvs)
	}
	caps, _ := result.Get("capabilities")
	info, _ := result.Get("serverInfo")
	c.mu.Lock()
	c.serverCapabilities, _ = caps.(*jsjson.Object)
	c.serverVersion, _ = info.(*jsjson.Object)
	if str, ok := result.GetString("instructions"); ok {
		c.instructions = &str
	}
	c.mu.Unlock()
	if setter, ok := transport.(ProtocolVersionSetter); ok {
		setter.SetProtocolVersion(pvs)
	}
	if err := c.Notification(ctx, "notifications/initialized", nil); err != nil {
		return err
	}
	c.mu.Lock()
	c.negotiated = pvs
	c.mu.Unlock()
	return nil
}

// Close closes the transport (which settles pending requests with ConnectionClosed) and resets
// the connection state.
func (c *Client) Close() error {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	var err error
	if t != nil {
		err = t.Close()
	}
	c.resetConnectionState()
	return err
}

func (c *Client) onclose() {
	c.mu.Lock()
	handlers := c.responseHandlers
	c.responseHandlers = map[float64]responseHandler{}
	c.progressHandlers = map[float64]func(*jsjson.Object){}
	for _, t := range c.timeouts {
		t.timer.Stop()
	}
	c.timeouts = map[float64]*pendingTimeout{}
	c.transport = nil
	f := c.OnClose
	c.mu.Unlock()
	defer func() {
		closed := &SdkError{Code: ErrConnectionClosed, Message: "Connection closed"}
		for _, h := range handlers {
			h(nil, closed)
		}
	}()
	if f != nil {
		f()
	}
}

func (c *Client) dispatch(m Message) {
	switch {
	case IsJSONRPCResultResponse(m) || IsJSONRPCErrorResponse(m):
		c.onresponse(m)
	case IsJSONRPCRequest(m):
		c.onrequest(m)
	case IsJSONRPCNotification(m):
		c.onnotification(m)
	default:
		c.onerror(fmt.Errorf("Unknown message type: %s", jsjson.Stringify(m)))
	}
}

func (c *Client) onresponse(m Message) {
	id, _ := MessageID(m)
	key, ok := idNumber(id)
	c.mu.Lock()
	h := c.responseHandlers[key]
	if !ok || h == nil {
		c.mu.Unlock()
		c.onerror(fmt.Errorf("Received a response for an unknown message ID: %s", jsjson.Stringify(m)))
		return
	}
	delete(c.responseHandlers, key)
	if t := c.timeouts[key]; t != nil {
		t.timer.Stop()
		delete(c.timeouts, key)
	}
	delete(c.progressHandlers, key)
	c.mu.Unlock()
	if IsJSONRPCResultResponse(m) {
		h(m, nil)
		return
	}
	errObj, _ := m.Get("error")
	eo := errObj.(*jsjson.Object)
	code, _ := eo.GetNumber("code")
	msg, _ := eo.GetString("message")
	data, hasData := eo.Get("data")
	pe := &ProtocolError{Code: int(code), Message: msg}
	if hasData && data != nil {
		pe.Data, pe.HasData = data, true
	}
	h(nil, pe)
}

// modernOnlyRequestMethods / Notifications are spec methods that exist only in the 2026-07-28
// registry; on a legacy connection they are refused locally (era gate).
var modernOnlyRequestMethods = map[string]bool{"server/discover": true, "subscriptions/listen": true, "tasks/update": true}

var modernOnlyNotificationMethods = map[string]bool{"notifications/subscriptions/acknowledged": true}

func (c *Client) sendErrorResponse(t Transport, id jsjson.Value, code int, message string) {
	resp := jsjson.Obj(
		jsjson.KV("jsonrpc", jsjson.String("2.0")),
		jsjson.KV("id", id),
		jsjson.KV("error", jsjson.Obj(jsjson.KV("code", jsjson.Int(int64(code))), jsjson.KV("message", jsjson.String(message)))),
	)
	go func() {
		if err := t.Send(context.Background(), resp, nil); err != nil {
			c.onerror(fmt.Errorf("Failed to send an error response: %s", jsErrorString(err)))
		}
	}()
}

// onrequest answers server-to-client requests: ping returns {}, everything else is "Method not
// found" (the CLI registers no other handlers).
func (c *Client) onrequest(m Message) {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == nil {
		return
	}
	method, _ := MessageMethod(m)
	id, _ := MessageID(m)
	if modernOnlyRequestMethods[method] || method != "ping" {
		c.sendErrorResponse(t, id, CodeMethodNotFound, "Method not found")
		return
	}
	if _, err := s.Parse(pingRequestWireSchema, m); err != nil {
		c.sendErrorResponse(t, id, CodeInternalError, err.Error())
		return
	}
	resp := jsjson.Obj(jsjson.KV("result", jsjson.NewObject()), jsjson.KV("jsonrpc", jsjson.String("2.0")), jsjson.KV("id", id))
	go func() {
		if err := t.Send(context.Background(), resp, nil); err != nil {
			c.onerror(fmt.Errorf("Failed to send response: %s", jsErrorString(err)))
		}
	}()
}

var cancelledNotificationWireSchema = s.Object(
	s.F("method", s.Literal("notifications/cancelled")),
	s.F("params", s.Object(
		s.F("_meta", s.Optional(requestMetaSchema)),
		s.F("requestId", s.Optional(requestIDSchema)),
		s.F("reason", s.Optional(s.String())),
	)),
)

func (c *Client) onnotification(m Message) {
	method, _ := MessageMethod(m)
	switch method {
	case "notifications/tools/list_changed":
		c.mu.Lock()
		c.cache.evict()
		c.mu.Unlock()
	}
	if modernOnlyNotificationMethods[method] {
		return
	}
	switch method {
	case "notifications/cancelled":
		if _, err := s.Parse(cancelledNotificationWireSchema, m); err != nil {
			c.onerror(fmt.Errorf("Uncaught error in notification handler: Error: %s", err.Error()))
		}
	case "notifications/progress":
		parsed, err := s.Parse(progressNotificationWireSchema, m)
		if err != nil {
			c.onerror(fmt.Errorf("Uncaught error in notification handler: Error: %s", err.Error()))
			return
		}
		c.onprogress(parsed.(*jsjson.Object))
	}
}

func (c *Client) onprogress(n *jsjson.Object) {
	p, _ := n.Get("params")
	params := p.(*jsjson.Object).Clone()
	token, _ := params.Get("progressToken")
	params.Delete("progressToken")
	key, _ := idNumber(token)
	c.mu.Lock()
	h := c.progressHandlers[key]
	if h == nil {
		c.mu.Unlock()
		c.onerror(fmt.Errorf("Received a progress notification for an unknown token: %s", jsjson.Stringify(n)))
		return
	}
	rh := c.responseHandlers[key]
	ti := c.timeouts[key]
	if ti != nil && rh != nil && ti.resetOnPrg {
		if ti.maxTotal > 0 && time.Since(ti.start) >= ti.maxTotal {
			delete(c.timeouts, key)
			delete(c.responseHandlers, key)
			delete(c.progressHandlers, key)
			ti.timer.Stop()
			c.mu.Unlock()
			rh(nil, &SdkError{Code: ErrRequestTimeout, Message: "Maximum total timeout exceeded", Data: map[string]any{"maxTotalTimeout": ti.maxTotal.Milliseconds(), "totalElapsed": time.Since(ti.start).Milliseconds()}})
			return
		}
		ti.timer.Reset(ti.timeout)
	}
	c.mu.Unlock()
	h(params)
}

func (c *Client) assertCapabilityForMethod(method string) error {
	has := func(k string) bool {
		if c.serverCapabilities == nil {
			return false
		}
		v, ok := c.serverCapabilities.Get(k)
		return ok && truthy(v)
	}
	need := ""
	switch method {
	case "logging/setLevel":
		need = "logging"
	case "prompts/get", "prompts/list":
		need = "prompts"
	case "resources/list", "resources/templates/list", "resources/read", "resources/subscribe", "resources/unsubscribe":
		need = "resources"
	case "tools/call", "tools/list":
		need = "tools"
	case "completion/complete":
		need = "completions"
	}
	if need != "" && !has(need) {
		return &SdkError{Code: ErrCapabilityNotSupported, Message: fmt.Sprintf("Server does not support %s (required for %s)", need, method)}
	}
	return nil
}

func truthy(v jsjson.Value) bool {
	switch t := v.(type) {
	case nil, jsjson.Null:
		return false
	case jsjson.Bool:
		return bool(t)
	case jsjson.Number:
		return t != 0 && t == t
	case jsjson.String:
		return t != ""
	}
	return true
}

// Request sends a request and validates its result with schema (nil: the 2025 registry schema
// for initialize, ping, tools/list and tools/call). params nil means no params key.
func (c *Client) Request(ctx context.Context, method string, params *jsjson.Object, schema s.Schema, opts RequestOptions) (jsjson.Value, error) {
	if schema == nil {
		switch method {
		case "initialize":
			schema = InitializeResultWireSchema
		case "ping":
			schema = EmptyResultWireSchema
		case "tools/list":
			schema = ListToolsResultWireSchema
		case "tools/call":
			schema = CallToolResultWireSchema
		default:
			return nil, fmt.Errorf("'%s' has no result schema in this port; pass a result schema to Request()", method)
		}
	}
	return c.request(ctx, method, params, schema, opts)
}

type outcome struct {
	value jsjson.Value
	err   error
}

func (c *Client) request(ctx context.Context, method string, params *jsjson.Object, schema s.Schema, opts RequestOptions) (jsjson.Value, error) {
	if modernOnlyRequestMethods[method] {
		return nil, &SdkError{Code: ErrMethodNotSupportedByProtocol, Message: fmt.Sprintf("Method '%s' is not supported by the negotiated protocol version (wire era 2025-11-25)", method), Data: map[string]any{"method": method, "era": "2025-11-25"}}
	}
	c.mu.Lock()
	t := c.transport
	if t == nil {
		c.mu.Unlock()
		return nil, errors.New("Not connected")
	}
	if c.opts.EnforceStrictCapabilities {
		if err := c.assertCapabilityForMethod(method); err != nil {
			c.mu.Unlock()
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, &SdkError{Code: ErrRequestTimeout, Message: jsErrorString(err)}
	}
	id := c.nextID
	c.nextID++
	key := float64(id)
	msg := jsjson.Obj(jsjson.KV("method", jsjson.String(method)))
	if params != nil {
		msg.Set("params", params)
	}
	msg.Set("jsonrpc", jsjson.String("2.0"))
	msg.Set("id", jsjson.Number(key))
	if opts.OnProgress != nil {
		c.progressHandlers[key] = opts.OnProgress
		p := jsjson.NewObject()
		if params != nil {
			p = params.Clone()
		}
		meta := jsjson.NewObject()
		if existing, ok := p.Get("_meta"); ok {
			if eo, ok := existing.(*jsjson.Object); ok {
				meta = eo.Clone()
			}
		}
		meta.Set("progressToken", jsjson.Number(key))
		p.Set("_meta", meta)
		msg.Set("params", p)
	}

	done := make(chan outcome, 1)
	var (
		settleMu         sync.Mutex
		settled          bool
		responseReceived bool
	)
	settle := func(o outcome) {
		settleMu.Lock()
		defer settleMu.Unlock()
		if settled {
			return
		}
		settled = true
		done <- o
	}
	cancel := func(reason error) {
		settleMu.Lock()
		received := responseReceived
		settleMu.Unlock()
		if received {
			return
		}
		c.mu.Lock()
		delete(c.progressHandlers, key)
		c.mu.Unlock()
		if method != "initialize" {
			note := jsjson.Obj(
				jsjson.KV("jsonrpc", jsjson.String("2.0")),
				jsjson.KV("method", jsjson.String("notifications/cancelled")),
				jsjson.KV("params", jsjson.Obj(jsjson.KV("requestId", jsjson.Number(key)), jsjson.KV("reason", jsjson.String(jsErrorString(reason))))),
			)
			go func() {
				if err := t.Send(context.Background(), note, nil); err != nil {
					c.onerror(fmt.Errorf("Failed to send cancellation: %s", jsErrorString(err)))
				}
			}()
		}
		var se *SdkError
		if errors.As(reason, &se) {
			settle(outcome{err: reason})
		} else {
			settle(outcome{err: &SdkError{Code: ErrRequestTimeout, Message: jsErrorString(reason)}})
		}
	}
	c.responseHandlers[key] = func(resp Message, err error) {
		if ctx.Err() != nil {
			return
		}
		settleMu.Lock()
		responseReceived = true
		settleMu.Unlock()
		if err != nil {
			settle(outcome{err: err})
			return
		}
		raw, _ := resp.Get("result")
		decoded := decodeResult2025(raw)
		parsed, perr := s.Parse(schema, decoded)
		if perr != nil {
			settle(outcome{err: &SdkError{Code: ErrInvalidResult, Message: fmt.Sprintf("Invalid result for %s: %s", method, perr.Error())}})
			return
		}
		settle(outcome{value: parsed})
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	pt := &pendingTimeout{start: time.Now(), timeout: timeout, maxTotal: opts.MaxTotalTimeout, resetOnPrg: opts.ResetTimeoutOnProgress}
	pt.onTimeout = func() { cancel(timeoutError(timeout.Milliseconds())) }
	pt.timer = time.AfterFunc(timeout, pt.onTimeout)
	c.timeouts[key] = pt
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.responseHandlers, key)
		if tt := c.timeouts[key]; tt != nil {
			tt.timer.Stop()
			delete(c.timeouts, key)
		}
		c.mu.Unlock()
	}()

	go func() {
		if err := t.Send(context.WithoutCancel(ctx), msg, nil); err != nil {
			c.mu.Lock()
			delete(c.progressHandlers, key)
			c.mu.Unlock()
			settle(outcome{err: err})
		}
	}()
	select {
	case o := <-done:
		return o.value, o.err
	case <-ctx.Done():
		cancel(ctx.Err())
		o := <-done
		return o.value, o.err
	}
}

// decodeResult2025 is the 2025 codec's decodeResult: a top-level resultType key is removed.
func decodeResult2025(raw jsjson.Value) jsjson.Value {
	o, ok := raw.(*jsjson.Object)
	if ok && o != nil && o.Has("resultType") {
		c := o.Clone()
		c.Delete("resultType")
		return c
	}
	return raw
}

// Notification sends a notification ({jsonrpc, method, params?}).
func (c *Client) Notification(ctx context.Context, method string, params *jsjson.Object) error {
	c.mu.Lock()
	t := c.transport
	c.mu.Unlock()
	if t == nil {
		return &SdkError{Code: ErrNotConnected, Message: "Not connected"}
	}
	if modernOnlyNotificationMethods[method] {
		return &SdkError{Code: ErrMethodNotSupportedByProtocol, Message: fmt.Sprintf("Notification '%s' is not supported by the negotiated protocol version (wire era 2025-11-25)", method)}
	}
	if method == "notifications/roots/list_changed" {
		ro, _ := c.caps.GetObject("roots")
		lc, _ := ro.Get("listChanged")
		if !truthy(lc) {
			return &SdkError{Code: ErrCapabilityNotSupported, Message: fmt.Sprintf("Client does not support roots list changed notifications (required for %s)", method)}
		}
	}
	msg := jsjson.Obj(jsjson.KV("jsonrpc", jsjson.String("2.0")), jsjson.KV("method", jsjson.String(method)))
	if params != nil {
		msg.Set("params", params)
	}
	return t.Send(ctx, msg, nil)
}

// Ping sends ping.
func (c *Client) Ping(ctx context.Context, opts RequestOptions) error {
	_, err := c.request(ctx, "ping", nil, EmptyResultWireSchema, opts)
	return err
}

func (c *Client) hasServerCapability(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.serverCapabilities == nil {
		return false
	}
	v, ok := c.serverCapabilities.Get(name)
	return ok && truthy(v)
}

// ListToolsParams are tools/list params. Cursor nil requests the auto-aggregating walk; Meta,
// when set, is sent as _meta on every page.
type ListToolsParams struct {
	Cursor *string
	Meta   *jsjson.Object
}

// ListTools is Client.listTools.
//
// Without a cursor it walks every page and returns ONE aggregated result: page 1's parsed
// result object (2025 wire schema: unknown top-level keys kept, tool objects stripped to the
// Tool schema's keys, outputSchema required to be type "object") with the tools of later pages
// appended and nextCursor deleted. The walk stops early when a page repeats the previous page's
// nextCursor and tools exactly; it fails with ListPaginationExceeded once ListMaxPages pages
// were fetched and another cursor remains. If the server did not advertise the tools capability
// it returns {"tools": []} without a request (unless EnforceStrictCapabilities is set, in which
// case it fails with CapabilityNotSupported). With a cursor it returns that single page.
func (c *Client) ListTools(ctx context.Context, params *ListToolsParams, opts RequestOptions) (*jsjson.Object, error) {
	if !c.hasServerCapability("tools") && !c.opts.EnforceStrictCapabilities {
		return jsjson.Obj(jsjson.KV("tools", jsjson.Array{})), nil
	}
	var base *jsjson.Object
	if params != nil && params.Meta != nil {
		base = jsjson.Obj(jsjson.KV("_meta", params.Meta))
	}
	if params != nil && params.Cursor != nil {
		p := jsjson.NewObject()
		if base != nil {
			p = base.Clone()
		}
		p.Set("cursor", jsjson.String(*params.Cursor))
		out, err := c.request(ctx, "tools/list", p, ListToolsResultWireSchema, opts)
		if err != nil {
			return nil, err
		}
		return out.(*jsjson.Object), nil
	}
	c.mu.Lock()
	hit := c.cache.read(time.Now())
	c.mu.Unlock()
	if hit != nil {
		return hit, nil
	}
	return c.listAllTools(ctx, base, opts)
}

func toolsOf(o *jsjson.Object) jsjson.Array {
	a, _ := o.GetArray("tools")
	return a
}

func cursorOf(o *jsjson.Object) (string, bool) {
	return o.GetString("nextCursor")
}

func (c *Client) listAllTools(ctx context.Context, base *jsjson.Object, opts RequestOptions) (*jsjson.Object, error) {
	c.mu.Lock()
	generation := c.cache.generation
	c.mu.Unlock()
	var first *jsjson.Object
	if base != nil {
		first = base.Clone()
	}
	out, err := c.request(ctx, "tools/list", first, ListToolsResultWireSchema, opts)
	if err != nil {
		return nil, err
	}
	acc := out.(*jsjson.Object)
	maxPages := c.opts.ListMaxPages
	if maxPages == 0 {
		maxPages = DefaultListMaxPages
	}
	cursor, hasCursor := cursorOf(acc)
	previous := acc
	pages := 1
	for hasCursor {
		if maxPages > 0 && pages >= maxPages {
			return nil, &SdkError{Code: ErrListPaginationExceeded, Message: fmt.Sprintf("tools/list: exceeded listMaxPages (%d); server pagination did not terminate", maxPages), Data: map[string]any{"method": "tools/list", "listMaxPages": maxPages}}
		}
		p := jsjson.NewObject()
		if base != nil {
			p = base.Clone()
		}
		p.Set("cursor", jsjson.String(cursor))
		pageOut, err := c.request(ctx, "tools/list", p, ListToolsResultWireSchema, opts)
		if err != nil {
			return nil, err
		}
		page := pageOut.(*jsjson.Object)
		next, hasNext := cursorOf(page)
		if hasNext && next == cursor && jsjson.Stringify(toolsOf(page)) == jsjson.Stringify(toolsOf(previous)) {
			break
		}
		acc.Set("tools", append(append(jsjson.Array{}, toolsOf(acc)...), toolsOf(page)...))
		previous = page
		cursor, hasCursor = next, hasNext
		pages++
	}
	acc.Delete("nextCursor")
	c.mu.Lock()
	c.cache.write(acc, generation, c.freshness(acc))
	c.mu.Unlock()
	return acc, nil
}

const maxCacheTTL = 24 * time.Hour

func (c *Client) freshness(result *jsjson.Object) time.Time {
	ttl := c.opts.DefaultCacheTTL
	if f, ok := result.GetNumber("ttlMs"); ok {
		ttl = time.Duration(f * float64(time.Millisecond))
	}
	if ttl < 0 {
		ttl = 0
	}
	if ttl > maxCacheTTL {
		ttl = maxCacheTTL
	}
	return time.Now().Add(ttl)
}

// CallToolParams are tools/call params, serialized as {name, arguments?, _meta?} (the CLI's
// literal order). Extra, when set, replaces the whole params object (for full control).
type CallToolParams struct {
	Name      string
	Arguments *jsjson.Object
	Meta      *jsjson.Object
	Extra     *jsjson.Object
}

func (p CallToolParams) object() *jsjson.Object {
	if p.Extra != nil {
		return p.Extra
	}
	o := jsjson.Obj(jsjson.KV("name", jsjson.String(p.Name)))
	if p.Arguments != nil {
		o.Set("arguments", p.Arguments)
	}
	if p.Meta != nil {
		o.Set("_meta", p.Meta)
	}
	return o
}

// CallTool is Client.callTool. What it does to the server's result (legacy era):
//  1. a top-level "resultType" key is removed;
//  2. if the result has no "content" key: it fails (InvalidResult) when it carries "task",
//     "inputRequests" or "requestState"; otherwise "content": [] is appended as the LAST key;
//  3. it is parsed with the 2025 CallToolResult schema: _meta (progressToken and related-task
//     first, other keys kept) then content, structuredContent, isError in that order, followed by
//     any unknown top-level keys in input order; every content block is stripped to its schema
//     (text: type, text, annotations, _meta; image/audio: type, data, mimeType, annotations,
//     _meta; resource_link: name, title, icons, uri, description, mimeType, size, annotations,
//     _meta, type; resource: type, resource{uri, mimeType, _meta, text|blob}, annotations,
//     _meta); annotations are stripped to audience, priority, lastModified; image/audio data and
//     blobs must pass atob; structuredContent must be an object (record) when present; isError
//     must be boolean. Any violation fails the call with SdkError InvalidResult;
//  4. output-schema validation runs only when this client holds a cached tools/list entry for
//     the tool with an outputSchema: then a non-error result must carry structuredContent that
//     validates (ProtocolError InvalidRequest / InvalidParams otherwise), and an uncompilable
//     outputSchema fails before the request (ProtocolError InvalidParams).
func (c *Client) CallTool(ctx context.Context, params CallToolParams, opts RequestOptions) (*jsjson.Object, error) {
	name := params.Name
	if params.Extra != nil {
		if _, ok := params.Extra.Get("name"); ok {
			name, _ = params.Extra.GetString("name")
		}
	}
	c.mu.Lock()
	compiled := c.cache.outputValidator(name, c.validator)
	c.mu.Unlock()
	if compiled != nil && compiled.err != nil {
		msg := compiled.err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, &ProtocolError{Code: CodeInvalidParams, Message: fmt.Sprintf("Tool '%s' has an invalid outputSchema: %s", name, msg)}
	}
	out, err := c.request(ctx, "tools/call", params.object(), CallToolResultWireSchema, opts)
	if err != nil {
		return nil, err
	}
	result := out.(*jsjson.Object)
	if compiled != nil && compiled.validate != nil {
		sc, hasSC := result.Get("structuredContent")
		isErr, _ := result.Get("isError")
		if !hasSC && !truthy(isErr) {
			return nil, &ProtocolError{Code: CodeInvalidRequest, Message: fmt.Sprintf("Tool %s has an output schema but did not return structured content", name)}
		}
		if hasSC && !truthy(isErr) {
			if ok, msg := compiled.validate(sc); !ok {
				return nil, &ProtocolError{Code: CodeInvalidParams, Message: "Structured content does not match the tool's output schema: " + msg}
			}
		}
	}
	return result, nil
}

// ---- response cache (tools/list only) ----

type compiledValidator struct {
	validate func(jsjson.Value) (bool, string)
	err      error
}

type toolsCache struct {
	entry      string // JSON document; "" when nothing is held
	expiresAt  time.Time
	stamp      int
	generation int
	indexStamp int
	index      map[string]*compiledValidator
}

func (tc *toolsCache) reset() {
	*tc = toolsCache{}
}

func (tc *toolsCache) evict() {
	tc.generation++
	tc.entry = ""
	tc.index = nil
}

func (tc *toolsCache) write(v *jsjson.Object, generation int, expiresAt time.Time) {
	if generation != tc.generation {
		return
	}
	tc.entry = jsjson.Stringify(v)
	tc.expiresAt = expiresAt
	tc.stamp++
}

func (tc *toolsCache) read(now time.Time) *jsjson.Object {
	if tc.entry == "" || !tc.expiresAt.After(now) {
		return nil
	}
	v, err := jsjson.ParseString(tc.entry)
	if err != nil {
		return nil
	}
	o, _ := v.(*jsjson.Object)
	return o
}

func (tc *toolsCache) outputValidator(name string, compiler JSONSchemaValidator) *compiledValidator {
	if tc.entry == "" {
		tc.index = nil
		return nil
	}
	if tc.index == nil || tc.indexStamp != tc.stamp {
		tc.index = map[string]*compiledValidator{}
		tc.indexStamp = tc.stamp
		v, err := jsjson.ParseString(tc.entry)
		if err == nil {
			if o, ok := v.(*jsjson.Object); ok {
				for _, t := range toolsOf(o) {
					tool, ok := t.(*jsjson.Object)
					if !ok {
						continue
					}
					ns, _ := tool.GetString("name")
					os, ok := tool.Get("outputSchema")
					if !ok {
						continue
					}
					schemaObj, ok := os.(*jsjson.Object)
					if !ok {
						continue
					}
					fn, err := compiler.Compile(schemaObj)
					tc.index[ns] = &compiledValidator{validate: fn, err: err}
				}
			}
		}
	}
	return tc.index[name]
}
