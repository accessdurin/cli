package mcp

import (
	"context"
	"errors"
	"io"
	"sync"
)

// StdioServerOptions are the StdioServerTransport options.
type StdioServerOptions struct {
	// MaxBufferSize bounds the buffered unconsumed input (default 10 MiB).
	MaxBufferSize int
	// ChunkSize is the read size, standing in for Node's stream chunking (default 64 KiB).
	ChunkSize int
}

// StdioServerTransport is the SDK's server-side stdio transport: newline-delimited JSON-RPC on
// an input stream (stdin) and an output stream (stdout).
//
// Input: each chunk is appended to a ReadBuffer; exceeding MaxBufferSize reports the error
// through onerror and closes the transport. Lines that are not JSON are skipped silently;
// JSON lines that are not JSON-RPC messages are reported through onerror (the transport stays
// open). End of input (or a read error, after onerror) closes the transport.
//
// Output: Send writes JSON.stringify(message) + "\n"; after Close it fails with
// "StdioServerTransport is closed". A write error fails the Send, is reported through onerror
// and closes the transport.
//
// Close stops delivering input and calls onclose once. A Read already blocked on the input is
// not interrupted (Go cannot cancel it); its data is discarded.
type StdioServerTransport struct {
	handlers handlerBox
	in       io.Reader
	out      io.Writer
	opts     StdioServerOptions

	mu      sync.Mutex
	started bool
	closed  bool
	rb      *ReadBuffer
	writeMu sync.Mutex
}

// NewStdioServerTransport creates the transport.
func NewStdioServerTransport(in io.Reader, out io.Writer, opts StdioServerOptions) *StdioServerTransport {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 64 * 1024
	}
	return &StdioServerTransport{in: in, out: out, opts: opts, rb: NewReadBuffer(opts.MaxBufferSize)}
}

// Handlers implements Transport.
func (t *StdioServerTransport) Handlers() Handlers { return t.handlers.get() }

// SetHandlers implements Transport.
func (t *StdioServerTransport) SetHandlers(h Handlers) { t.handlers.set(h) }

// SessionID implements Transport (stdio has no session).
func (t *StdioServerTransport) SessionID() string { return "" }

func (t *StdioServerTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// Start begins reading the input.
func (t *StdioServerTransport) Start(context.Context) error {
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return errors.New("StdioServerTransport already started! If using Server class, note that connect() calls start() automatically.")
	}
	t.started = true
	t.mu.Unlock()
	go t.readLoop()
	return nil
}

func (t *StdioServerTransport) readLoop() {
	buf := make([]byte, t.opts.ChunkSize)
	for {
		n, err := t.in.Read(buf)
		if t.isClosed() {
			return
		}
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if !t.ondata(chunk) {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.handlers.err(err)
			}
			_ = t.Close()
			return
		}
	}
}

func (t *StdioServerTransport) ondata(chunk []byte) bool {
	t.mu.Lock()
	err := t.rb.Append(chunk)
	t.mu.Unlock()
	if err != nil {
		t.handlers.err(err)
		_ = t.Close()
		return false
	}
	for {
		if t.isClosed() {
			return false
		}
		t.mu.Lock()
		m, err := t.rb.ReadMessage()
		t.mu.Unlock()
		if err != nil {
			t.handlers.err(err)
			continue
		}
		if m == nil {
			return true
		}
		t.handlers.message(m)
	}
}

// Close implements Transport.
func (t *StdioServerTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.rb.Clear()
	t.mu.Unlock()
	t.handlers.close()
	return nil
}

// Send implements Transport.
func (t *StdioServerTransport) Send(_ context.Context, m Message, _ *SendOptions) error {
	if t.isClosed() {
		return errors.New("StdioServerTransport is closed")
	}
	line, err := serializeLine(m)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	_, err = t.out.Write(line)
	t.writeMu.Unlock()
	if err != nil {
		if !t.isClosed() {
			t.handlers.err(err)
			_ = t.Close()
		}
		return err
	}
	return nil
}
