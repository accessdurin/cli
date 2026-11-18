// Package observer is durin's always-on helper: it receives hook events from harnesses over
// a local owner-only channel, notices connections to AI providers, and keeps an encrypted,
// bounded local capture of both. Nothing it collects leaves the machine.
package observer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxFrame bounds one message on the channel: a hook payload (at most 1 MiB, as hooks read
// it) plus its envelope.
const MaxFrame = 2 << 20

// Message types.
const (
	TypeEvent  = "event"
	TypeStatus = "status"
	TypePurge  = "purge"
)

// Message is one frame from a client. Event messages carry a hook event; a status message
// asks the observer for Stats, which it sends back as one frame.
type Message struct {
	Type    string          `json:"type"`
	Harness string          `json:"harness,omitempty"`
	Event   string          `json:"event,omitempty"`
	Time    time.Time       `json:"ts,omitzero"`
	PID     int             `json:"pid,omitempty"`  // the hook process
	PPID    int             `json:"ppid,omitempty"` // usually the harness, or a shell it started
	Payload json.RawMessage `json:"payload,omitempty"`
}

// WriteFrame writes a 4-byte big-endian length and data.
func WriteFrame(w io.Writer, data []byte) error {
	if len(data) > MaxFrame {
		return errors.New("observer: frame too large")
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data)))
	copy(buf[4:], data)
	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one frame written by WriteFrame.
func ReadFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n > MaxFrame {
		return nil, errors.New("observer: frame too large")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}

// EventPayload is the hook payload as the envelope carries it: the JSON itself when it is
// valid JSON, otherwise a JSON string of the raw text.
func EventPayload(raw []byte) json.RawMessage {
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	quoted, _ := json.Marshal(string(raw))
	return quoted
}

// Address is where the observer for a configuration directory listens: a Unix socket under
// <config>/observer, or (when that path is too long for a socket) a per-user directory under
// the temporary directory; on Windows a named pipe derived from the directory.
func Address(configDir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(configDir))))
	id := hex.EncodeToString(sum[:8])
	if isWindows {
		return `\\.\pipe\durin-observer-` + id
	}
	path := filepath.Join(configDir, "observer", "observer.sock")
	// sun_path holds 104 bytes on macOS and 108 on Linux, including the terminator.
	if len(path) < 100 {
		return path
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("durin-%d", os.Getuid()), id+".sock")
}

// Send delivers one event to the observer and returns without waiting for it to be
// processed. It gives up when ctx ends; callers bound it tightly so that a missing or stuck
// observer never delays a harness.
func Send(ctx context.Context, address string, message Message) error {
	message.Type = TypeEvent
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	conn, err := dial(ctx, address)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	done := make(chan error, 1)
	go func() { done <- WriteFrame(conn, data) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Query asks a running observer for its Stats.
func Query(ctx context.Context, address string) (Stats, error) {
	var stats Stats
	data, err := request(ctx, address, TypeStatus)
	if err != nil {
		return stats, err
	}
	return stats, json.Unmarshal(data, &stats)
}

// PurgeResult is a running observer's answer to a purge request.
type PurgeResult struct {
	Deleted int    `json:"deleted"`
	Error   string `json:"error,omitempty"`
}

// RequestPurge asks a running observer to delete its whole capture, closing the segment it
// is writing first.
func RequestPurge(ctx context.Context, address string) (PurgeResult, error) {
	var result PurgeResult
	data, err := request(ctx, address, TypePurge)
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(data, &result)
}

func request(ctx context.Context, address, kind string) ([]byte, error) {
	conn, err := dial(ctx, address)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	message, _ := json.Marshal(Message{Type: kind})
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		if err := WriteFrame(conn, message); err != nil {
			done <- result{err: err}
			return
		}
		data, err := ReadFrame(bufio.NewReader(conn))
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
