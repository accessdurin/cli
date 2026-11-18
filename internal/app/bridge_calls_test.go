package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp"
)

type memoryLedger map[string]contracts.PendingCliCall

func (m memoryLedger) Read(fingerprint string) (*contracts.PendingCliCall, error) {
	if call, ok := m[fingerprint]; ok {
		return &call, nil
	}
	return nil, nil
}
func (m memoryLedger) Save(fingerprint string, call contracts.PendingCliCall) error {
	m[fingerprint] = call
	return nil
}
func (m memoryLedger) Remove(fingerprint string) error { delete(m, fingerprint); return nil }

func message(t *testing.T, text string) mcp.Message {
	t.Helper()
	m, err := mcp.ParseMessageJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func callRequest(id string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"github.create_issue","arguments":{"title":"x","n":[1,2]}}}`
}

func control(t *testing.T, m mcp.Message) string {
	t.Helper()
	params, _ := m.GetObject("params")
	meta, _ := params.GetObject("_meta")
	value, _ := meta.Get(controlKey)
	return jsjson.Stringify(value)
}

func outgoing(t *testing.T, calls *BridgeCalls, text string) mcp.Message {
	t.Helper()
	m, err := calls.Outgoing(message(t, text))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func deliver(t *testing.T, calls *BridgeCalls, text string) string {
	t.Helper()
	var delivered string
	if err := calls.Deliver(message(t, text), func(m mcp.Message) error { delivered = jsjson.Stringify(m); return nil }); err != nil {
		t.Fatal(err)
	}
	return delivered
}

func TestBridgeCallsResumeAnApprovalAcrossRestarts(t *testing.T) {
	for _, structured := range []string{
		`{"status":"approval_required","approvalId":"approval_one"}`,
		`{"result":{"status":"approval_required","approvalId":"approval_one"}}`,
	} {
		dir := tempDir(t)
		ledger, err := NewFileCallLedger(dir, strings.Repeat("a", 64), nil)
		if err != nil {
			t.Fatal(err)
		}
		first := outgoing(t, NewBridgeCalls(ledger), callRequest("1"))
		key := control(t, first)
		if !strings.HasPrefix(key, `{"idempotencyKey":"cli_`) {
			t.Fatalf("control %s", key)
		}
		// The fingerprint is the SHA-256 of the canonical call, so key order does not matter
		// and a restarted bridge resumes the same operation (this is the e2e golden's entry).
		if saved, err := ledger.Read("b9287c04c1f0f84cbf38e5be1a4d17a0f1cac514b1cdb901609c97bdce75e2a8"); err != nil || saved == nil {
			t.Fatalf("ledger entry %v, %v", saved, err)
		}
		resumed := NewBridgeCalls(ledger)
		second := outgoing(t, resumed, `{"jsonrpc":"2.0","id":"two","method":"tools/call","params":{"arguments":{"n":[1,2],"title":"x"},"name":"github.create_issue"}}`)
		if control(t, second) != key {
			t.Fatalf("resumed %s, want %s", control(t, second), key)
		}
		delivered := deliver(t, resumed, `{"jsonrpc":"2.0","id":"two","result":{"content":[],"isError":true,"structuredContent":`+structured+`}}`)
		if want := `"_meta":{"io.durin/control":` + key + `}`; !strings.Contains(delivered, want) {
			t.Errorf("delivered %s without %s", delivered, want)
		}
		calls := NewBridgeCalls(ledger)
		third := outgoing(t, calls, callRequest("3"))
		if want := strings.TrimSuffix(key, "}") + `,"approvalId":"approval_one"}`; control(t, third) != want {
			t.Errorf("third call control %s, want %s", control(t, third), want)
		}
		// The approval covers one attempt: a failed approved call keeps the key only.
		deliver(t, calls, `{"jsonrpc":"2.0","id":3,"result":{"content":[],"isError":true}}`)
		if got := control(t, outgoing(t, NewBridgeCalls(ledger), callRequest("5"))); got != key {
			t.Errorf("after a failed approved call: %s, want %s", got, key)
		}
	}
}

func TestBridgeCallsResolveOnlyDeliveredFinalSuccesses(t *testing.T) {
	ledger := memoryLedger{}
	calls := NewBridgeCalls(ledger)
	key := control(t, outgoing(t, calls, callRequest("1")))
	// An input_required result is answered by repeating the same call.
	deliver(t, calls, `{"jsonrpc":"2.0","id":1,"result":{"content":[],"resultType":"input_required"}}`)
	if control(t, outgoing(t, calls, callRequest("2"))) != key {
		t.Fatal("input_required ended the operation")
	}
	// A success whose delivery fails stays pending.
	err := calls.Deliver(message(t, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"done"}]}}`), func(mcp.Message) error { return errors.New("stdio closed") })
	if err == nil || len(ledger) != 1 {
		t.Fatalf("failed delivery: %v, ledger %v", err, ledger)
	}
	outgoing(t, calls, callRequest("3"))
	deliver(t, calls, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"done"}],"extra":"kept"}}`)
	if len(ledger) != 0 {
		t.Fatalf("a delivered success stayed pending: %v", ledger)
	}

	// An error response forgets the request but keeps the operation; a later result for the
	// same id is not mistaken for it.
	outgoing(t, calls, callRequest("4"))
	deliver(t, calls, `{"jsonrpc":"2.0","id":4,"error":{"code":-32000,"message":"upstream failed"}}`)
	if got := deliver(t, calls, `{"jsonrpc":"2.0","id":4,"result":{"content":[]}}`); strings.Contains(got, controlKey) || len(ledger) != 1 {
		t.Fatalf("untracked result %s, ledger %v", got, ledger)
	}
}

func TestBridgeCallsLeaveAgentControlsAndUnsafeApprovalsAlone(t *testing.T) {
	ledger := memoryLedger{}
	calls := NewBridgeCalls(ledger)
	for _, text := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","arguments":{},"_meta":{"io.durin/control":{"idempotencyKey":"agent-key"}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"a","arguments":{"durinIdempotencyKey":"legacy"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"a"}}`,
	} {
		in := message(t, text)
		out, err := calls.Outgoing(in)
		if err != nil || jsjson.Stringify(out) != jsjson.Stringify(in) {
			t.Errorf("changed %s into %s (%v)", text, jsjson.Stringify(out), err)
		}
	}
	if len(ledger) != 0 {
		t.Fatalf("recorded %v", ledger)
	}

	// An approval ID that is unsafe on a command line is never stored or replayed.
	key := control(t, outgoing(t, calls, callRequest("4")))
	deliver(t, calls, `{"jsonrpc":"2.0","id":4,"result":{"content":[],"structuredContent":{"status":"approval_required","approvalId":"x; rm -rf ~"}}}`)
	if got := control(t, outgoing(t, calls, callRequest("5"))); got != key {
		t.Errorf("replayed %s, want %s", got, key)
	}
}
