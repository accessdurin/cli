package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// CallRetention: an unresolved operation is resumed only this long after its last state change.
const CallRetention = 24 * time.Hour

var pendingCallSchema = zod.Object(
	zod.F("idempotencyKey", zod.String().Min(1)),
	zod.F("approvalId", zod.Optional(zod.String())),
).Strict()

// FileCallLedger keeps pending bridge operations in <directory>/calls/<profile>/<fingerprint>.json.
// Only operation identifiers and a hash of the request are persisted, never tool arguments.
type FileCallLedger struct {
	directory string
	now       func() time.Time
}

var _ ports.CliCallLedger = (*FileCallLedger)(nil)

// NewFileCallLedger returns the ledger for a profile; now may be nil (time.Now).
func NewFileCallLedger(directory, profileID string, now func() time.Time) (*FileCallLedger, error) {
	if _, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(profileID)); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &FileCallLedger{directory: filepath.Join(directory, "calls", profileID), now: now}, nil
}

// Read returns the pending operation for fingerprint; nil when there is none or it expired.
func (l *FileCallLedger) Read(fingerprint string) (*contracts.PendingCliCall, error) {
	path, err := l.path(fingerprint)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if missing(err) {
			return nil, nil
		}
		return nil, err
	}
	if l.now().Sub(info.ModTime()) >= CallRetention {
		return nil, rmForce(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if missing(err) {
			return nil, nil
		}
		return nil, err
	}
	value, err := jsjson.Parse(data)
	if err != nil {
		return nil, err
	}
	out, err := zod.Parse(pendingCallSchema, value)
	if err != nil {
		return nil, err
	}
	o := out.(*jsjson.Object)
	call := &contracts.PendingCliCall{}
	call.IdempotencyKey, _ = o.GetString("idempotencyKey")
	call.ApprovalID, _ = o.GetString("approvalId")
	return call, nil
}

// Save records the pending operation for fingerprint.
func (l *FileCallLedger) Save(fingerprint string, call contracts.PendingCliCall) error {
	if err := os.MkdirAll(l.directory, 0o700); err != nil {
		return err
	}
	path, err := l.path(fingerprint)
	if err != nil {
		return err
	}
	value := jsjson.Obj(jsjson.KV("idempotencyKey", jsjson.String(call.IdempotencyKey)))
	if call.ApprovalID != "" {
		value.Set("approvalId", jsjson.String(call.ApprovalID))
	}
	parsed, err := zod.Parse(pendingCallSchema, value)
	if err != nil {
		return err
	}
	temporary := path + "." + randomUUID()
	defer rmForce(temporary)
	if err := writeExclusive(temporary, []byte(jsjson.Stringify(parsed)), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// Remove forgets the operation for fingerprint.
func (l *FileCallLedger) Remove(fingerprint string) error {
	path, err := l.path(fingerprint)
	if err != nil {
		return err
	}
	return rmForce(path)
}

func (l *FileCallLedger) path(fingerprint string) (string, error) {
	if _, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(fingerprint)); err != nil {
		return "", err
	}
	return filepath.Join(l.directory, fingerprint+".json"), nil
}

var (
	approvalSchema = zod.Object(
		zod.F("status", zod.Literal(jsjson.String("approval_required"))),
		zod.F("approvalId", zod.String().Min(1)),
	)
	approvalContentSchema = zod.Union(
		approvalSchema,
		zod.Transform(zod.Object(zod.F("result", approvalSchema)), func(v jsjson.Value) jsjson.Value {
			result, _ := v.(*jsjson.Object).Get("result")
			return result
		}),
	)
	legacyControlSchema = zod.Object(zod.F("durinIdempotencyKey", zod.String()))
)

type trackedCall struct {
	fingerprint string
	control     contracts.PendingCliCall
}

// BridgeCalls is BridgeCalls (src/bridge-calls.ts): it gives each native tools/call an
// idempotency key that survives restarts of the bridge, keyed by a fingerprint of the call,
// and keeps an approval the server asks for until the call is repeated.
type BridgeCalls struct {
	ledger ports.CliCallLedger

	mu       sync.Mutex
	requests map[string]trackedCall // by JSON.stringify(id), as a Map keeps 1 and "1" apart
}

// NewBridgeCalls returns BridgeCalls over ledger.
func NewBridgeCalls(ledger ports.CliCallLedger) *BridgeCalls {
	return &BridgeCalls{ledger: ledger, requests: map[string]trackedCall{}}
}

func requestKey(id jsjson.Value) string { return jsjson.Stringify(id) }

func controlObject(control contracts.PendingCliCall) *jsjson.Object {
	o := jsjson.Obj(jsjson.KV("idempotencyKey", jsjson.String(control.IdempotencyKey)))
	if control.ApprovalID != "" {
		o.Set("approvalId", jsjson.String(control.ApprovalID))
	}
	return o
}

// Outgoing prepares an agent's message for the gateway: a native tools/call without a
// control gets the operation's idempotency key (and saved approval) in
// _meta["io.durin/control"].
func (c *BridgeCalls) Outgoing(message mcp.Message) (mcp.Message, error) {
	if mcp.IsJSONRPCRequest(message) {
		id, _ := mcp.MessageID(message)
		c.mu.Lock()
		delete(c.requests, requestKey(id))
		c.mu.Unlock()
	}
	if method, _ := mcp.MessageMethod(message); method != "tools/call" || !mcp.IsJSONRPCRequest(message) {
		return message, nil
	}
	params, ok := message.GetObject("params")
	if !ok {
		params = jsjson.NewObject()
	}
	if hasControl(params) {
		return message, nil
	}
	fingerprint := callFingerprint(params)
	control, err := c.operation(fingerprint)
	if err != nil {
		return nil, err
	}
	id, _ := mcp.MessageID(message)
	c.mu.Lock()
	c.requests[requestKey(id)] = trackedCall{fingerprint, control}
	c.mu.Unlock()
	meta, _ := params.GetObject("_meta")
	return message.Clone().Set("params", params.Clone().Set("_meta", meta.Clone().Set(controlKey, controlObject(control)))), nil
}

// operation records a new operation before it is sent; resuming one never extends its
// retention.
func (c *BridgeCalls) operation(fingerprint string) (contracts.PendingCliCall, error) {
	pending, err := c.ledger.Read(fingerprint)
	if err != nil {
		return contracts.PendingCliCall{}, err
	}
	if pending != nil {
		return *pending, nil
	}
	control := contracts.PendingCliCall{IdempotencyKey: "cli_" + randomUUID()}
	return control, c.ledger.Save(fingerprint, control)
}

// Deliver passes a gateway message to the agent through send. A result for a tracked call
// carries its control in _meta; its approval is persisted before delivery, and a resolved
// operation is forgotten after it.
func (c *BridgeCalls) Deliver(message mcp.Message, send func(mcp.Message) error) error {
	if mcp.IsJSONRPCErrorResponse(message) {
		if id, ok := mcp.MessageID(message); ok {
			c.mu.Lock()
			delete(c.requests, requestKey(id))
			c.mu.Unlock()
		}
	}
	if !mcp.IsJSONRPCResultResponse(message) {
		return send(message)
	}
	id, _ := mcp.MessageID(message)
	c.mu.Lock()
	tracked, ok := c.requests[requestKey(id)]
	c.mu.Unlock()
	if !ok {
		return send(message)
	}
	result, _ := message.GetObject("result")
	resolved, err := c.recordOutcome(result, tracked)
	if err != nil {
		return err
	}
	meta, _ := result.GetObject("_meta")
	delivered := message.Clone().Set("result", result.Clone().Set("_meta", meta.Clone().Set(controlKey, controlObject(tracked.control))))
	if err := send(delivered); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.requests, requestKey(id))
	c.mu.Unlock()
	if resolved {
		return c.ledger.Remove(tracked.fingerprint)
	}
	return nil
}

// recordOutcome persists approval state before delivery and reports whether the operation
// resolved.
func (c *BridgeCalls) recordOutcome(result *jsjson.Object, tracked trackedCall) (bool, error) {
	content, _ := result.Get("structuredContent")
	if approval, err := zod.SafeParse(approvalContentSchema, content); err == nil {
		approvalID, _ := approval.(*jsjson.Object).GetString("approvalId")
		return false, c.ledger.Save(tracked.fingerprint, withApproval(tracked.control, approvalID))
	}
	if finalSuccess(result) {
		return true, nil
	}
	if isError, _ := result.Get("isError"); isError == jsjson.Bool(true) && tracked.control.ApprovalID != "" {
		// An approval covers one attempt: a failed approved call keeps its key, not its approval.
		return false, c.ledger.Save(tracked.fingerprint, contracts.PendingCliCall{IdempotencyKey: tracked.control.IdempotencyKey})
	}
	return false, nil
}

// withApproval: agents pass approval IDs on command lines, so only shell-inert identifiers
// are resumed.
func withApproval(control contracts.PendingCliCall, approvalID string) contracts.PendingCliCall {
	if contracts.ValidCliIdentifier(approvalID) {
		return contracts.PendingCliCall{IdempotencyKey: control.IdempotencyKey, ApprovalID: approvalID}
	}
	return contracts.PendingCliCall{IdempotencyKey: control.IdempotencyKey}
}

// finalSuccess: an input_required result is answered by repeating the same call, so it stays
// pending. A created task is resolved here: its outcome arrives through tasks/result, not a new
// call.
func finalSuccess(result *jsjson.Object) bool {
	content, hasContent := result.Get("content")
	_, isArray := content.(jsjson.Array)
	resultType := "complete"
	if value, ok := result.Get("resultType"); ok && value != nil {
		if _, null := value.(jsjson.Null); !null {
			text, isString := value.(jsjson.String)
			resultType = string(text)
			if !isString {
				resultType = "\x00" // any non-string value is not 'complete'
			}
		}
	}
	isError, _ := result.Get("isError")
	return (!hasContent || content == nil || isArray) && resultType == "complete" && isError != jsjson.Bool(true)
}

func callFingerprint(params *jsjson.Object) string {
	name, _ := params.Get("name")
	arguments, ok := params.Get("arguments")
	if !ok || arguments == nil {
		arguments = jsjson.NewObject()
	} else if _, null := arguments.(jsjson.Null); null {
		arguments = jsjson.NewObject()
	}
	sum := sha256.Sum256([]byte(jsjson.CanonicalJSON(jsjson.Obj(jsjson.KV("name", name), jsjson.KV("arguments", arguments)))))
	return hex.EncodeToString(sum[:])
}

func hasControl(params *jsjson.Object) bool {
	if meta, ok := params.GetObject("_meta"); ok && meta.Has(controlKey) {
		return true
	}
	arguments, _ := params.Get("arguments")
	return zod.Valid(legacyControlSchema, arguments)
}
