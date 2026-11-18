package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
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
// (The BridgeCalls logic that uses it belongs to the MCP step of the port.)
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
