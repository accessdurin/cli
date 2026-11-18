package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/mcp/schema"
)

type stdioEvent struct {
	Message *string `json:"message,omitempty"`
	Error   *string `json:"error,omitempty"`
	Close   bool    `json:"close,omitempty"`
}

type stdioFixture struct {
	Framing    map[string][]stdioEvent `json:"framing"`
	Predicates []struct {
		Value        string `json:"value"`
		Request      bool   `json:"request"`
		Notification bool   `json:"notification"`
		Result       bool   `json:"result"`
		Error        bool   `json:"error"`
		Initialize   bool   `json:"initialize"`
		Initialized  bool   `json:"initialized"`
	} `json:"predicates"`
}

func errorName(err error) string {
	var se *schema.Error
	var syn *jsonv.SyntaxError
	switch {
	case errors.As(err, &se):
		return "ZodError"
	case errors.As(err, &syn):
		return "SyntaxError"
	}
	return "Error"
}

// stdioLines must match the `lines` corpus in testdata/stdio.mjs.
var stdioLines = []string{
	`{"params":{"b":1,"_meta":{"x":1,"progressToken":"t","io.modelcontextprotocol/related-task":{"taskId":"q","extra":1}},"a":2},"method":"tools/call","id":"x","jsonrpc":"2.0"}`,
	`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	`{"id":1,"result":{"z":1,"_meta":{"b":1,"io.modelcontextprotocol/serverInfo":{"name":"a","version":"1","x":1}},"a":2},"jsonrpc":"2.0"}`,
	`{"id":2,"jsonrpc":"2.0","result":{"_meta":{"io.modelcontextprotocol/serverInfo":{"name":1}}}}`,
	`{"id":1,"jsonrpc":"2.0","error":{"message":"m","code":-1,"data":{"x":1},"extra":2}}`,
	`{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse"}}`,
	`{"id":null,"jsonrpc":"2.0","error":{"message":"m","code":1}}`,
	`not json at all`,
	``,
	`[{"jsonrpc":"2.0","method":"a"}]`,
	`{"jsonrpc":"2.0","method":"n","params":null}`,
	"{\"jsonrpc\":\"2.0\",\"method\":\"crlf\"}\r",
	`{"jsonrpc":"2.0","id":1.0,"method":"one"}`,
	`{"jsonrpc":"2.0","id":1e2,"method":"hundred"}`,
	`{"jsonrpc":"2.0","id":9007199254740993,"method":"big"}`,
	`{"jsonrpc":"2.0","id":1.5,"method":"frac"}`,
	`{"jsonrpc":"2.0","id":-0,"method":"negzero"}`,
	`{"jsonrpc":"2.0","method":"dup","method":"dup2","id":3}`,
	`{"jsonrpc":"2.0","id":4,"method":"m","params":{"b":1,"2":2,"1":1,"__proto__":{"x":1}}}`,
	`  {"jsonrpc":"2.0","id":5,"method":"ws"}  `,
	`{"jsonrpc":"2.0","id":6,"method":"m","params":{"s":"\ud800 lone \u00e9 \u2028 \u0001"}}`,
	`{"jsonrpc":"2.0","id":7,"method":"m","extra":1}`,
	`{"jsonrpc":"2.1","id":8,"method":"m"}`,
	`{"jsonrpc":"2.0","id":9,"result":[]}`,
	`{"jsonrpc":"2.0","id":10,"method":"m","params":{"n":1e400,"f":0.1,"g":1e21,"h":1e-7,"i":123456789012345680000}}`,
	`{"jsonrpc":"2.0","id":11,"method":"m","params":[1,2]}`,
	`{"jsonrpc":"2.0","id":12,"method":"m","params":{"_meta":{"progressToken":1.5}}}`,
}

func feedStdio(t *testing.T, chunks [][]byte, maxBuffer int) []stdioEvent {
	t.Helper()
	pr, pw := io.Pipe()
	var out bytes.Buffer
	tr := NewStdioServerTransport(pr, &out, StdioServerOptions{MaxBufferSize: maxBuffer})
	var mu sync.Mutex
	var events []stdioEvent
	tr.SetHandlers(Handlers{
		OnMessage: func(m Message) {
			s := jsonv.MustStringify(m)
			mu.Lock()
			events = append(events, stdioEvent{Message: &s})
			mu.Unlock()
		},
		OnError: func(err error) {
			n := errorName(err)
			mu.Lock()
			events = append(events, stdioEvent{Error: &n})
			mu.Unlock()
		},
		OnClose: func() {
			mu.Lock()
			events = append(events, stdioEvent{Close: true})
			mu.Unlock()
		},
	})
	if err := tr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	go func() {
		for _, c := range chunks {
			if _, err := pw.Write(c); err != nil {
				return
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	return append([]stdioEvent(nil), events...)
}

// TestStdioServerTransportMatchesSDK replays testdata/fixtures/stdio.json (recorded from the
// SDK's StdioServerTransport with maxBufferSize 1_048_576): messages are re-serialized in zod's
// key order, non-JSON lines are skipped silently, schema-invalid lines raise onerror, an
// oversized buffer raises onerror and closes.
func TestStdioServerTransportMatchesSDK(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures/stdio.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx stdioFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, l := range stdioLines {
		all.WriteString(l + "\n")
	}
	invalid := append(append([]byte(`{"jsonrpc":"2.0","id":13,"method":"bad`), 0xff, 0xc3), []byte("\"}\n")...)
	cases := map[string][][]byte{
		"lines":       {[]byte(all.String())},
		"invalidUtf8": {invalid},
		"split":       {[]byte(`{"jsonrpc":"2.0",`), []byte("\"method\":\"split\"}\n{\"jsonrpc\""), []byte(":\"2.0\",\"method\":\"next\"}\n")},
		"oversize":    {[]byte(`{"jsonrpc":"2.0","method":"big","params":{"s":"` + strings.Repeat("a", 1_048_576) + "\"}}\n"), []byte(`{"jsonrpc":"2.0","method":"after"}` + "\n")},
	}
	for name, chunks := range cases {
		got := feedStdio(t, chunks, 1_048_576)
		want := fx.Framing[name]
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(want)
		if string(gj) != string(wj) {
			t.Errorf("%s:\n got %s\n SDK %s", name, gj, wj)
		}
	}
}

func TestPredicatesMatchSDK(t *testing.T) {
	raw, err := os.ReadFile("testdata/fixtures/stdio.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx stdioFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Predicates) < 20 {
		t.Fatalf("only %d predicate cases", len(fx.Predicates))
	}
	for _, p := range fx.Predicates {
		v, err := jsonv.ParseString(p.Value)
		if err != nil {
			t.Fatal(err)
		}
		got := [6]bool{IsJSONRPCRequest(v), IsJSONRPCNotification(v), IsJSONRPCResultResponse(v), IsJSONRPCErrorResponse(v), IsInitializeRequest(v), IsInitializedNotification(v)}
		want := [6]bool{p.Request, p.Notification, p.Result, p.Error, p.Initialize, p.Initialized}
		if got != want {
			t.Errorf("%s: request/notification/result/error/initialize/initialized = %v, SDK %v", p.Value, got, want)
		}
	}
}

func TestStdioServerTransportSendAndClose(t *testing.T) {
	pr, pw := io.Pipe()
	var out bytes.Buffer
	tr := NewStdioServerTransport(pr, &out, StdioServerOptions{})
	closed := make(chan struct{})
	tr.SetHandlers(Handlers{OnClose: func() { close(closed) }})
	if err := tr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tr.Start(context.Background()); err == nil {
		t.Fatal("second Start succeeded")
	}
	m, _ := ParseMessageJSON([]byte(`{"jsonrpc":"2.0","id":1,"result":{"s":"\ud800"}}`))
	if err := tr.Send(context.Background(), m, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"jsonrpc":"2.0","id":1,"result":{"s":"\ud800"}}`+"\n" {
		t.Fatalf("wrote %q", out.String())
	}
	_ = pw.Close() // end of input closes the transport
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("end of input did not close the transport")
	}
	if err := tr.Send(context.Background(), m, nil); err == nil || err.Error() != "StdioServerTransport is closed" {
		t.Fatalf("send after close: %v", err)
	}
}
