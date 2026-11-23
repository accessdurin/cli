package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// SpoolLimits bound the report spool (src/report-spool.ts). Calls wait in the spool until a
// background flush delivers them, so hooks never block on the network. Each profile-lock
// hold covers one connection and at most Chunk calls, so the Durin bridge, which needs the
// same lock, is never kept waiting for a whole spool.
var SpoolLimits = struct {
	Files                     int
	Age, Lock, Temporary      time.Duration
	Chunk, PerFlush, Attempts int
	Backoff, MaxBackoff       time.Duration
}{
	Files: 200, Age: 7 * 24 * time.Hour, Lock: 2 * time.Minute, Temporary: time.Hour,
	Chunk: 5, PerFlush: 50, Attempts: 5, Backoff: time.Minute, MaxBackoff: time.Hour,
}

// SpoolDirectory is <directory>/reporting/spool/<sha256(profile id)>.
func SpoolDirectory(directory, profileID string) string {
	sum := sha256.Sum256([]byte(profileID))
	return filepath.Join(directory, "reporting", "spool", hex.EncodeToString(sum[:]))
}

type spoolEntry struct {
	path, name string
	at         time.Time
	attempt    int
}

// spoolCall writes a call to the spool as <reportId>.json, then trims the spool. It returns
// the call's path.
func spoolCall(spool string, call *jsjson.Object, now func() time.Time) (string, error) {
	parsed, err := zod.Parse(contracts.LocalToolCallSchema, call)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(spool, 0o700); err != nil {
		return "", err
	}
	temporary := filepath.Join(spool, "."+randomUUID()+".tmp")
	if err := writeExclusive(temporary, []byte(jsjson.Stringify(parsed)), 0o600); err != nil {
		return "", err
	}
	id, _ := parsed.(*jsjson.Object).GetString("reportId")
	path := filepath.Join(spool, id+".json")
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	_, err = trimSpool(spool, now)
	return path, err
}

func listedSpool(spool string) []spoolEntry {
	names, err := os.ReadDir(spool)
	if err != nil {
		return nil
	}
	var out []spoolEntry
	for _, name := range names {
		path := filepath.Join(spool, name.Name())
		if info, err := os.Stat(path); err == nil {
			out = append(out, spoolEntry{path: path, name: name.Name(), at: info.ModTime()})
		}
	}
	return out
}

func pendingSpool(entries []spoolEntry) []spoolEntry {
	var out []spoolEntry
	for _, entry := range entries {
		match := reportFileName.FindStringSubmatch(entry.name)
		if match == nil {
			continue
		}
		entry.attempt, _ = strconv.Atoi(match[2])
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out
}

// trimSpool keeps the newest calls within budget and drops old ones and leftovers of
// interrupted writes. It returns the calls that remain, oldest first.
func trimSpool(spool string, now func() time.Time) ([]spoolEntry, error) {
	entries := listedSpool(spool)
	files := pendingSpool(entries)
	excess := len(files) - SpoolLimits.Files
	var kept []spoolEntry
	for i, file := range files {
		if i < excess || file.at.Before(now().Add(-SpoolLimits.Age)) {
			_ = rmForce(file.path)
			continue
		}
		kept = append(kept, file)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.name, ".tmp") && entry.at.Before(now().Add(-SpoolLimits.Temporary)) {
			_ = rmForce(entry.path)
		}
	}
	return kept, nil
}

// acquireFlushLock takes the flush lock, replacing it once if a crashed flush left it behind.
func acquireFlushLock(lock string) bool {
	if os.Mkdir(lock, 0o700) == nil {
		return true
	}
	info, err := os.Stat(lock)
	if err != nil || time.Since(info.ModTime()) < SpoolLimits.Lock {
		return false
	}
	_ = os.RemoveAll(lock)
	return os.Mkdir(lock, 0o700) == nil
}

var backoffSchema = zod.Object(zod.F("until", zod.Number()), zod.F("failures", zod.Number().Int().Min(0)))

type spoolBackoff struct {
	until    float64 // epoch milliseconds
	failures int
}

func backoffPath(spool string) string { return filepath.Join(spool, ".backoff.json") }

func readBackoff(spool string) spoolBackoff {
	raw, err := os.ReadFile(backoffPath(spool))
	if err != nil {
		return spoolBackoff{}
	}
	value, err := jsjson.ParseString(string(raw))
	if err != nil {
		return spoolBackoff{}
	}
	out, zerr := zod.SafeParse(backoffSchema, value)
	if zerr != nil {
		return spoolBackoff{}
	}
	o := out.(*jsjson.Object)
	until, _ := o.GetNumber("until")
	failures, _ := o.GetNumber("failures")
	return spoolBackoff{until: until, failures: int(failures)}
}

func epochMs(t time.Time) float64 { return float64(t.UnixMilli()) }

// recordOutcome clears the backoff after a success; after a failure flushes back off
// exponentially from SpoolLimits.Backoff up to SpoolLimits.MaxBackoff.
func recordOutcome(spool string, failed bool, now func() time.Time) error {
	if !failed {
		return rmForce(backoffPath(spool))
	}
	failures := readBackoff(spool).failures + 1
	delay := math.Min(float64(SpoolLimits.Backoff.Milliseconds())*math.Pow(2, float64(failures-1)), float64(SpoolLimits.MaxBackoff.Milliseconds()))
	body := jsjson.Stringify(jsjson.Obj(
		jsjson.KV("until", jsjson.Number(epochMs(now())+delay)),
		jsjson.KV("failures", jsjson.Int(int64(failures))),
	))
	return os.WriteFile(backoffPath(spool), []byte(body), 0o600)
}

// ToolCaller is the part of an MCP client a flush needs.
type ToolCaller interface {
	CallTool(ctx context.Context, input contracts.CliToolCall) (contracts.McpToolResult, error)
}

// SpoolConnect runs work on one connection, under the profile lock.
type SpoolConnect func(work func(client ToolCaller) error) error

type delivery int

const (
	deliverySent delivery = iota
	deliveryRejected
	deliveryDropped
)

// rejectedCall retries a call the gateway refused (for example, a gateway without the tool
// yet) a few times, then drops it.
func rejectedCall(file spoolEntry) error {
	if file.attempt+1 >= SpoolLimits.Attempts {
		return rmForce(file.path)
	}
	base := strings.TrimSuffix(file.path, ".json")
	if file.attempt > 0 {
		base = strings.TrimSuffix(base, "."+strconv.Itoa(file.attempt))
	}
	return os.Rename(file.path, base+"."+strconv.Itoa(file.attempt+1)+".json")
}

func deliverReport(ctx context.Context, client ToolCaller, file spoolEntry) (delivery, error) {
	raw, _ := os.ReadFile(file.path)
	value, err := jsjson.ParseString(string(raw))
	var call jsjson.Value
	if err == nil {
		call, _ = zod.SafeParse(contracts.LocalToolCallSchema, value)
	}
	parsed, ok := call.(*jsjson.Object)
	if !ok {
		return deliveryDropped, rmForce(file.path)
	}
	id, _ := parsed.GetString("reportId")
	result, err := client.CallTool(ctx, contracts.CliToolCall{
		Name:           contracts.McpReportLocalToolCallTool,
		Arguments:      jsjson.Obj(jsjson.KV("call", parsed)),
		IdempotencyKey: id,
	})
	if err != nil {
		return 0, err
	}
	if jsjson.Equal(get(result, "isError"), jsjson.Bool(true)) {
		return deliveryRejected, rejectedCall(file)
	}
	return deliverySent, rmForce(file.path)
}

// flushRounds sends the spool in chunks until it is empty, the per-flush cap is reached, or a
// call is rejected. It re-lists the spool before each chunk, so calls queued during the flush
// are picked up.
func flushRounds(ctx context.Context, spool string, connect SpoolConnect, now func() time.Time) (sent int, failed bool, err error) {
	for sent < SpoolLimits.PerFlush {
		files, err := trimSpool(spool, now)
		if err != nil {
			return sent, false, err
		}
		if len(files) > SpoolLimits.Chunk {
			files = files[:SpoolLimits.Chunk]
		}
		if len(files) == 0 {
			return sent, false, nil
		}
		var results []delivery
		err = connect(func(client ToolCaller) error {
			for _, file := range files {
				result, err := deliverReport(ctx, client, file)
				if err != nil {
					return err
				}
				results = append(results, result)
			}
			return nil
		})
		for _, result := range results {
			if result == deliverySent {
				sent++
			}
		}
		if err != nil {
			return sent, false, err
		}
		for _, result := range results {
			if result == deliveryRejected {
				return sent, true, nil
			}
		}
	}
	return sent, false, nil
}

// FlushSpool delivers spooled calls and returns how many were sent. After a failure, flushes
// back off exponentially up to an hour. An error (offline, signed out) is returned after the
// backoff is recorded; the remaining calls stay for a later flush.
func FlushSpool(ctx context.Context, spool string, connect SpoolConnect, now func() time.Time) (int, error) {
	if readBackoff(spool).until > epochMs(now()) {
		return 0, nil
	}
	lock := filepath.Join(spool, ".flush.lock")
	if !acquireFlushLock(lock) {
		return 0, nil
	}
	defer os.Remove(lock)
	sent, failed, err := flushRounds(ctx, spool, connect, now)
	if err != nil {
		_ = recordOutcome(spool, true, now)
		return sent, err
	}
	return sent, recordOutcome(spool, failed, now)
}
