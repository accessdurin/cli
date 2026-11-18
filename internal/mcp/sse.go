package mcp

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
)

// sseEvent is an event emitted by eventsource-parser.
type sseEvent struct {
	ID    string // "" when unset
	Event string // "" when unset
	Data  string
}

var retryDigits = regexp.MustCompile(`^\d+$`)

// parseSSE ports eventsource-parser 3.1.1 as the SDK uses it (TextDecoderStream piped into
// EventSourceParserStream with only onRetry):
//   - lines end at CR, LF or CRLF; a leading UTF-8 BOM is dropped (TextDecoder);
//   - an event is dispatched on a blank line only if it had at least one data line; the blank
//     line resets id, event and data even when nothing is dispatched (ids do not persist across
//     events, unlike browser EventSource);
//   - "id" values containing NUL are ignored; "retry" with only digits calls onRetry;
//   - a trailing event without its blank line is dropped at end of stream.
//
// It returns the read error that ended the stream (nil at a clean EOF).
func parseSSE(r io.Reader, onEvent func(sseEvent), onRetry func(int)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var (
		id, eventType, data string
		dataLines           int
		line                []byte
		first               = true
		pendingCR           bool
	)
	dispatch := func() {
		if dataLines > 0 {
			onEvent(sseEvent{ID: id, Event: eventType, Data: data})
		}
		id, eventType, data, dataLines = "", "", "", 0
	}
	processLine := func(raw []byte) {
		l := jsonv.DecodeUTF8(raw)
		if l == "" {
			dispatch()
			return
		}
		var field, value string
		switch {
		case strings.HasPrefix(l, ":"):
			return
		default:
			idx := strings.IndexByte(l, ':')
			if idx == -1 {
				field, value = l, ""
			} else {
				field = l[:idx]
				value = l[idx+1:]
				value = strings.TrimPrefix(value, " ")
			}
		}
		switch field {
		case "event":
			eventType = value
		case "data":
			if dataLines == 0 {
				data = value
			} else {
				data = data + "\n" + value
			}
			dataLines++
		case "id":
			if !strings.Contains(value, "\x00") {
				id = value
			}
		case "retry":
			if retryDigits.MatchString(value) {
				if n, err := strconv.Atoi(value); err == nil && onRetry != nil {
					onRetry(n)
				}
			}
		}
	}
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if first {
			first = false
			if b == 0xEF {
				peek, _ := br.Peek(2)
				if bytes.Equal(peek, []byte{0xBB, 0xBF}) {
					_, _ = br.Discard(2)
					continue
				}
			}
		}
		if pendingCR {
			pendingCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\r':
			processLine(line)
			line = line[:0]
			pendingCR = true
		case '\n':
			processLine(line)
			line = line[:0]
		default:
			line = append(line, b)
		}
	}
}
