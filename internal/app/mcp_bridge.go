package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// signOutCheckInterval is how often an open bridge checks whether another process left
// sign-out unfinished.
const signOutCheckInterval = time.Second

// maxBridgeInput bounds the agent's buffered, unconsumed input (1 MiB).
const maxBridgeInput = 1_048_576

const bridgeRequestFailed = "Durin MCP request failed. Its outcome may be uncertain; retain the same idempotency key when retrying."

// McpStdioBridgeOptions configure an McpStdioBridge. Calls, Serialize, OnError and Marker
// may be nil. Warn receives the SDK's warnings (nil: stderr through the sanitized console).
type McpStdioBridgeOptions struct {
	Resource      string
	Authorization oauth.ClientProvider
	Fetch         httpx.Fetch
	Input         io.Reader
	Output        io.Writer
	Development   bool
	Calls         *BridgeCalls
	Serialize     func(work func() error) error
	OnError       func(message string)
	Marker        ports.RevocationMarker
	Warn          func(string)
}

func bridgeFailureMessage(err error) string {
	if e, ok := clierror.As(err); ok {
		return e.Message
	}
	var unauthorized *oauth.UnauthorizedError
	if errors.As(err, &unauthorized) {
		return SignInAgain
	}
	return bridgeRequestFailed
}

// deliveryChain is the bridge's promise chain of deliveries: each runs after the previous
// one, and once one fails the rest are skipped and the chain stays failed.
type deliveryChain struct {
	mu   sync.Mutex
	tail chan struct{}
	err  error
}

func (c *deliveryChain) then(work func() error, onFailure func()) {
	c.mu.Lock()
	previous, done := c.tail, make(chan struct{})
	c.tail = done
	c.mu.Unlock()
	go func() {
		defer close(done)
		if previous != nil {
			<-previous
		}
		c.mu.Lock()
		failed := c.err
		c.mu.Unlock()
		if failed == nil {
			if err := work(); err != nil {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
				failed = err
			}
		}
		if failed != nil {
			onFailure()
		}
	}()
}

// wait is `await this.delivery`: it waits for every delivery queued so far.
func (c *deliveryChain) wait() error {
	c.mu.Lock()
	tail := c.tail
	c.mu.Unlock()
	if tail != nil {
		<-tail
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// McpStdioBridge is McpStdioBridge (src/mcp-bridge.ts): it relays an agent's MCP session on
// stdio to the organization's gateway, one message at a time under the profile lock.
type McpStdioBridge struct {
	options McpStdioBridgeOptions
	local   *mcp.StdioServerTransport
	remote  *mcp.StreamableHTTPClientTransport

	delivery deliveryChain
	forwards sync.WaitGroup
	done     chan struct{}

	mu                 sync.Mutex
	initializedRequest jsjson.Value
	forwardTail        chan struct{}
	closed             bool
	signedOut          bool
}

// NewMcpStdioBridge fails for a resource that is not a safe CLI URL.
func NewMcpStdioBridge(options McpStdioBridgeOptions) (*McpStdioBridge, error) {
	remote, err := newMcpTransport(options.Resource, options.Development, options.Authorization, options.Fetch, options.Warn)
	if err != nil {
		return nil, err
	}
	b := &McpStdioBridge{
		options:   options,
		local:     mcp.NewStdioServerTransport(options.Input, options.Output, mcp.StdioServerOptions{MaxBufferSize: maxBridgeInput}),
		remote:    remote,
		done:      make(chan struct{}),
		signedOut: true,
	}
	b.local.SetHandlers(mcp.Handlers{
		OnMessage: b.received,
		OnError: func(error) {
			b.report("Invalid MCP input.")
			_ = b.Close()
		},
		OnClose: func() { _ = b.Close() },
	})
	b.remote.SetHandlers(mcp.Handlers{
		OnMessage: func(message mcp.Message) {
			b.negotiated(message)
			b.delivery.then(func() error { return b.deliver(message) }, func() { _ = b.Close() })
		},
		OnError: func(error) { b.report("Durin MCP transport failed.") },
		OnClose: func() { _ = b.Close() },
	})
	return b, nil
}

// Start opens both transports and starts watching for an unfinished sign-out.
func (b *McpStdioBridge) Start(ctx context.Context) error {
	if err := b.remote.Start(ctx); err != nil {
		return err
	}
	if err := b.local.Start(ctx); err != nil {
		return err
	}
	b.watchSignOut()
	return nil
}

// Close closes both transports once.
func (b *McpStdioBridge) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	_ = b.remote.Close()
	_ = b.local.Close()
	close(b.done)
	return nil
}

// Wait blocks until the bridge is closed and every message it was forwarding has settled, so
// no profile lock is left behind.
func (b *McpStdioBridge) Wait() {
	<-b.done
	b.forwards.Wait()
}

// Done is closed once the bridge is closed.
func (b *McpStdioBridge) Done() <-chan struct{} { return b.done }

func (b *McpStdioBridge) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// received queues each agent message behind the previous one, so the gateway sees them in
// the agent's order: notifications/initialized before the next request, a cancellation after
// the request it cancels. The profile lock alone does not keep that order; its waiters poll,
// and a notification used to lose to every later request until the bridge closed (#28's
// TestBridge failures).
func (b *McpStdioBridge) received(message mcp.Message) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	if method, _ := mcp.MessageMethod(message); method == "initialize" && mcp.IsJSONRPCRequest(message) {
		b.initializedRequest, _ = mcp.MessageID(message)
	}
	b.forwards.Add(1)
	previous, done := b.forwardTail, make(chan struct{})
	b.forwardTail = done
	b.mu.Unlock()
	go func() {
		defer b.forwards.Done()
		defer close(done)
		if previous != nil {
			<-previous
		}
		if err := b.forward(message); err != nil {
			_ = b.Close()
		}
	}()
}

// watchSignOut: once another process leaves sign-out unfinished, the bridge closes, ending
// its event stream and any token refresh. A bridge that starts while sign-out is unfinished
// stays open, so that each call explains why it is refused.
func (b *McpStdioBridge) watchSignOut() {
	if b.options.Marker == nil {
		return
	}
	check := func() {
		if err := b.closeOnSignOut(); err != nil {
			_ = b.Close()
		}
	}
	check()
	go func() {
		ticker := time.NewTicker(signOutCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-b.done:
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}

func (b *McpStdioBridge) closeOnSignOut() error {
	signedOut, err := b.signOutPending()
	if err != nil {
		return err
	}
	b.mu.Lock()
	started := !b.signedOut && signedOut
	b.signedOut = signedOut
	b.mu.Unlock()
	if started {
		b.closeSignedOut()
	}
	return nil
}

func (b *McpStdioBridge) signOutPending() (bool, error) {
	if b.options.Marker == nil {
		return false, nil
	}
	return b.options.Marker.Pending()
}

func (b *McpStdioBridge) closeSignedOut() {
	if b.isClosed() {
		return
	}
	b.report(SignOutUnfinished)
	_ = b.Close()
}

func (b *McpStdioBridge) forward(message mcp.Message) error {
	work := func() error { return b.send(message) }
	var err error
	if b.options.Serialize != nil {
		err = b.options.Serialize(work)
	} else {
		err = work()
	}
	if err != nil {
		return b.failed(message, err)
	}
	return nil
}

func (b *McpStdioBridge) send(message mcp.Message) error {
	prepared := message
	if b.options.Calls != nil {
		var err error
		if prepared, err = b.options.Calls.Outgoing(message); err != nil {
			return err
		}
	}
	if err := b.remote.Send(context.Background(), prepared, nil); err != nil {
		return err
	}
	return b.delivery.wait()
}

// deliver: nothing the server sends reaches the agent while sign-out is unfinished.
func (b *McpStdioBridge) deliver(message mcp.Message) error {
	pending, err := b.signOutPending()
	if err != nil {
		return err
	}
	if pending {
		b.closeSignedOut()
		return nil
	}
	send := func(m mcp.Message) error { return b.local.Send(context.Background(), m, nil) }
	if b.options.Calls != nil {
		return b.options.Calls.Deliver(message, send)
	}
	return send(message)
}

// failed answers a request that could not be relayed with a JSON-RPC error; for anything
// else it only reports the failure.
func (b *McpStdioBridge) failed(message mcp.Message, err error) error {
	if !mcp.IsJSONRPCRequest(message) {
		b.report(bridgeFailureMessage(err))
		return nil
	}
	id, _ := mcp.MessageID(message)
	return b.local.Send(context.Background(), jsjson.Obj(
		jsjson.KV("jsonrpc", jsjson.String("2.0")),
		jsjson.KV("id", id),
		jsjson.KV("error", jsjson.Obj(jsjson.KV("code", jsjson.Int(-32000)), jsjson.KV("message", jsjson.String(bridgeFailureMessage(err))))),
	), nil)
}

// negotiated: once the gateway answers the agent's initialize, later requests carry the
// protocol version it chose.
func (b *McpStdioBridge) negotiated(message mcp.Message) {
	out, err := zod.SafeParse(contracts.CliMcpNegotiationSchema, message)
	if err != nil {
		return
	}
	parsed := out.(*jsjson.Object)
	id, _ := parsed.Get("id")
	b.mu.Lock()
	initialized := b.initializedRequest
	b.mu.Unlock()
	if initialized == nil || !jsjson.Equal(id, initialized) {
		return
	}
	result, _ := parsed.GetObject("result")
	version, _ := result.GetString("protocolVersion")
	b.remote.SetProtocolVersion(version)
}

func (b *McpStdioBridge) report(message string) {
	if b.options.OnError != nil {
		b.options.OnError(message)
	}
}
