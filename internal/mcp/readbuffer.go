package mcp

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
)

// StdioDefaultMaxBufferSize is STDIO_DEFAULT_MAX_BUFFER_SIZE (10 MiB).
const StdioDefaultMaxBufferSize = 10 * 1024 * 1024

// ReadBuffer is the SDK's ReadBuffer: it splits a byte stream into newline-delimited JSON-RPC
// messages.
//
//   - Append fails, and clears the buffer, when the unconsumed bytes plus the new chunk exceed
//     the maximum size ("ReadBuffer exceeded maximum size of N bytes"). The limit applies to the
//     bytes buffered at once, so it depends on how the input arrives in chunks, exactly as in Node.
//   - ReadMessage decodes each line as UTF-8 (invalid sequences become U+FFFD), strips one
//     trailing CR, and parses it with JSON.parse + JSONRPCMessageSchema. A line that is not JSON
//     is skipped silently; a JSON line that is not a valid JSON-RPC message consumes the line and
//     returns the schema error (the transports report it through onerror and keep reading).
type ReadBuffer struct {
	buf     []byte
	has     bool
	maxSize int
}

// NewReadBuffer creates a buffer; maxSize <= 0 means StdioDefaultMaxBufferSize.
func NewReadBuffer(maxSize int) *ReadBuffer {
	if maxSize <= 0 {
		maxSize = StdioDefaultMaxBufferSize
	}
	return &ReadBuffer{maxSize: maxSize}
}

// Append adds a chunk.
func (r *ReadBuffer) Append(chunk []byte) error {
	if len(r.buf)+len(chunk) > r.maxSize {
		r.Clear()
		return fmt.Errorf("ReadBuffer exceeded maximum size of %d bytes", r.maxSize)
	}
	r.buf = append(r.buf, chunk...)
	r.has = true
	return nil
}

// ReadMessage returns the next message, nil when no complete line is buffered.
func (r *ReadBuffer) ReadMessage() (Message, error) {
	for r.has {
		idx := bytes.IndexByte(r.buf, '\n')
		if idx == -1 {
			return nil, nil
		}
		line := strings.TrimSuffix(jsonv.DecodeUTF8(r.buf[:idx]), "\r")
		r.buf = r.buf[idx+1:]
		v, err := jsonv.ParseString(line)
		if err != nil {
			var se *jsonv.SyntaxError
			if errors.As(err, &se) {
				continue
			}
			return nil, err
		}
		m, err := ParseMessage(v)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, nil
}

// Clear drops buffered data.
func (r *ReadBuffer) Clear() {
	r.buf = nil
	r.has = false
}

func serializeLine(m Message) ([]byte, error) {
	b, err := SerializeMessage(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
