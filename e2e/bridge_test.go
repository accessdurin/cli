package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Bridge drives `durin mcp serve` over stdio like an agent would.
type Bridge struct {
	e      *Env
	stdin  io.WriteCloser
	lines  chan string
	stderr *strings.Builder
	done   chan int
	log    strings.Builder
	pings  int
}

func (e *Env) StartBridge(args ...string) *Bridge {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	e.t.Cleanup(cancel)
	cmd := e.command(ctx, append([]string{"mcp", "serve"}, args...))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	b := &Bridge{e: e, stdin: stdin, lines: make(chan string, 64), stderr: &strings.Builder{}, done: make(chan int, 1)}
	cmd.Stderr = b.stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 4<<20)
		for scanner.Scan() {
			b.lines <- scanner.Text()
		}
		close(b.lines)
	}()
	go func() {
		err := cmd.Wait()
		code := 0
		if exit, ok := err.(interface{ ExitCode() int }); ok && err != nil {
			code = exit.ExitCode()
		}
		b.done <- code
	}()
	b.log.WriteString("## durin mcp serve " + strings.Join(args, " ") + "\n")
	return b
}

// Send writes one line to the bridge.
func (b *Bridge) Send(line string) {
	b.log.WriteString("> " + line + "\n")
	_, _ = io.WriteString(b.stdin, line+"\n")
}

// Receive waits for one line from the bridge.
func (b *Bridge) Receive() string {
	select {
	case line, ok := <-b.lines:
		if !ok {
			b.log.WriteString("< (closed)\n")
			return ""
		}
		b.log.WriteString("< " + canonicalLine(line) + "\n")
		return line
	case <-time.After(30 * time.Second):
		b.e.t.Fatalf("bridge did not answer; transcript so far:\n%s\nstderr:\n%s", b.log.String(), b.stderr.String())
		return ""
	}
}

// canonicalLine sorts JSON keys: the order of keys in a JSON-RPC message carries no meaning.
func canonicalLine(line string) string {
	var value any
	if json.Unmarshal([]byte(line), &value) != nil {
		return line
	}
	out, _ := json.Marshal(value)
	return string(out)
}

// settle waits for a ping round-trip: the bridge delivers in order, so once the ping is
// answered every earlier response has been delivered and its ledger update finished.
func (b *Bridge) settle() {
	b.pings++
	b.Send(`{"jsonrpc":"2.0","id":"ping-` + strconv.Itoa(b.pings) + `","method":"ping"}`)
	b.Receive()
}

// Close ends stdin and records the exit status and stderr.
func (b *Bridge) Close() {
	_ = b.stdin.Close()
	b.Finish()
}

// Finish waits for the bridge to exit on its own.
func (b *Bridge) Finish() {
	select {
	case code := <-b.done:
		for line := range b.lines {
			b.log.WriteString("< " + canonicalLine(line) + "\n")
		}
		b.log.WriteString("exit: " + strconv.Itoa(code) + "\n--- stderr\n" + ensureNewline(b.stderr.String()))
	case <-time.After(30 * time.Second):
		b.e.t.Fatalf("bridge did not exit; transcript:\n%s", b.log.String())
	}
	b.e.transcript.WriteString(b.log.String())
}

// ledger lists the bridge's pending-call ledger for a profile with each entry's content.
func (e *Env) ledger(id string) string {
	dir := filepath.Join(e.Config, "calls", id)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "<no ledger directory>\n"
	}
	var lines []string
	for _, entry := range entries {
		info, _ := entry.Info()
		data, _ := os.ReadFile(filepath.Join(dir, entry.Name()))
		lines = append(lines, entry.Name()+" "+info.Mode().Perm().String()+" "+string(data))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return "<empty>\n"
	}
	return strings.Join(lines, "\n") + "\n"
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"agent","version":"1"}}}`

func TestBridge(t *testing.T) {
	e, id := signedIn(t)
	e.Server.Pages = [][]Tool{sampleTools()}
	e.Server.CallReplies["github.create_issue"] = []CallReply{
		{Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "needs approval"}}, "structuredContent": map[string]any{"status": "approval_required", "approvalId": "appr_7"}}},
		{Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}, "extra": "kept"}},
		{Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "explicit"}}}},
		{Error: map[string]any{"code": -32000, "message": "upstream failed"}},
		{Result: map[string]any{"content": []any{map[string]any{"type": "text", "text": "failed"}}, "isError": true}},
	}
	b := e.StartBridge("--profile", id, "--config-dir", e.Config)
	b.Send(initialize)
	b.Receive()
	b.Send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	b.Send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	b.Receive()
	b.Send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github.create_issue","arguments":{"title":"x","n":[1,2]}}}`)
	b.Receive()
	b.settle()
	b.log.WriteString("## ledger after approval_required\n" + e.ledger(id))
	b.Send(`{"jsonrpc":"2.0","id":"four","method":"tools/call","params":{"arguments":{"n":[1,2],"title":"x"},"name":"github.create_issue"}}`)
	b.Receive()
	b.settle()
	b.log.WriteString("## ledger after success\n" + e.ledger(id))
	b.Send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"github.create_issue","arguments":{"title":"y"},"_meta":{"io.durin/control":{"idempotencyKey":"agent-key"}}}}`)
	b.Receive()
	b.Send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"github.create_issue","arguments":{"title":"z"}}}`)
	b.Receive()
	b.settle()
	b.log.WriteString("## ledger after error response\n" + e.ledger(id))
	b.Send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"github.create_issue","arguments":{"title":"z"}}}`)
	b.Receive()
	b.settle()
	b.log.WriteString("## ledger after isError\n" + e.ledger(id))
	b.Send(`{"jsonrpc":"2.0","id":9,"method":"no/such"}`)
	b.Receive()
	b.Close()
	e.NoteRequests()
}

func TestBridgeFailures(t *testing.T) {
	e, id := signedIn(t)
	// Invalid input closes the bridge.
	b := e.StartBridge("--profile", id, "--config-dir", e.Config)
	b.Send(initialize)
	b.Receive()
	b.Send(`this is not json`)
	b.Close()
	e.Server.ResetLog()

	// A request the server answers with 500 gets a JSON-RPC error; a notification only a report.
	e.Server.MCPHook = func(method string, _ []byte) bool { return method == "tools/list" }
	b = e.StartBridge("--profile", id, "--config-dir", e.Config)
	b.Send(initialize)
	b.Receive()
	b.Send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	b.Receive()
	b.Close()
	e.Server.MCPHook = nil
	e.Server.ResetLog()

	// Without a usable grant, each request is answered with the sign-in explanation.
	_ = os.Remove(filepath.Join(e.Config, "active"))
	other := e.DefaultProfile()
	other.PrincipalID = "user_09"
	otherID := e.SaveProfile(other)
	b = e.StartBridge("--profile", otherID, "--config-dir", e.Config)
	b.Send(initialize)
	b.Receive()
	b.Close()
	e.NoteRequests()

	// While sign-out is unfinished, the bridge refuses requests.
	e.WriteFile(filepath.Join(e.Config, "revocations", id+".pending"), "{}\n", 0o600)
	b = e.StartBridge("--profile", id, "--config-dir", e.Config)
	b.Send(initialize)
	b.Receive()
	b.Close()
	e.NoteRequests()
}
