package mcp

import (
	"context"
	"sync"
)

// Handlers are a transport's onmessage / onerror / onclose callbacks.
//
// Concurrency: JavaScript runs these on one event loop. Here a transport invokes them from the
// goroutine that reads a given stream (the POST response, an SSE stream, stdin), so callbacks
// for one stream are ordered, but callbacks for different streams may run concurrently.
type Handlers struct {
	OnMessage func(Message)
	OnError   func(error)
	OnClose   func()
}

// SendOptions are TransportSendOptions.
type SendOptions struct {
	// RelatedRequestID is accepted for parity; the client transports ignore it.
	RelatedRequestID any
	// ResumptionToken resumes an SSE stream (GET with Last-Event-ID) instead of POSTing.
	ResumptionToken string
	// OnResumptionToken receives each SSE event id.
	OnResumptionToken func(string)
	// Headers are extra per-request headers; transport-owned names are ignored.
	Headers map[string]string
	// OnRequestStreamEnd is called when the request's response stream ends.
	OnRequestStreamEnd func()
}

// Transport is the SDK's Transport interface.
type Transport interface {
	Start(ctx context.Context) error
	Send(ctx context.Context, message Message, opts *SendOptions) error
	Close() error
	Handlers() Handlers
	SetHandlers(Handlers)
	// SessionID returns the session id, or "" when there is none.
	SessionID() string
}

// ProtocolVersionSetter is implemented by transports that send mcp-protocol-version.
type ProtocolVersionSetter interface {
	SetProtocolVersion(version string)
}

type handlerBox struct {
	mu sync.RWMutex
	h  Handlers
}

func (b *handlerBox) get() Handlers {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.h
}

func (b *handlerBox) set(h Handlers) {
	b.mu.Lock()
	b.h = h
	b.mu.Unlock()
}

func (b *handlerBox) message(m Message) {
	if f := b.get().OnMessage; f != nil {
		f(m)
	}
}

func (b *handlerBox) err(e error) {
	if f := b.get().OnError; f != nil {
		f(e)
	}
}

func (b *handlerBox) close() {
	if f := b.get().OnClose; f != nil {
		f()
	}
}
