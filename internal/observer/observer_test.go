package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryKeys struct {
	mu     sync.Mutex
	values map[string]string
	fail   bool
}

func (m *memoryKeys) Read(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return "", false, errors.New("locked")
	}
	v, ok := m.values[key]
	return v, ok, nil
}

func (m *memoryKeys) Write(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("locked")
	}
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[key] = value
	return nil
}

func (m *memoryKeys) Remove(key string) error { delete(m.values, key); return nil }

type fakeTable struct {
	mu    sync.Mutex
	conns []Conn
}

func (f *fakeTable) set(conns ...Conn) { f.mu.Lock(); f.conns = conns; f.mu.Unlock() }
func (f *fakeTable) Connections() ([]Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Conn(nil), f.conns...), nil
}

type fakeProcs map[int]Process

func (f fakeProcs) Process(pid int) (Process, bool) { p, ok := f[pid]; return p, ok }

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "obs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The full path: hooks over the owner-only socket, status and purge requests, provider
// detection from a fake socket table and resolver with process and harness attribution,
// the traffic hook, and an encrypted capture that only the stored key opens.
func TestObserverRoundTrip(t *testing.T) {
	dir := shortDir(t)
	keys := &memoryKeys{}
	table := &fakeTable{}
	openai := netip.MustParseAddr("192.0.2.10")
	procs := fakeProcs{
		42: {PID: 42, PPID: 41, Exe: "/usr/bin/node", Args: []string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}},
		41: {PID: 41, PPID: 1, Exe: "/bin/zsh"},
		77: {PID: 77, PPID: 76, Exe: "/usr/bin/python3"},
		76: {PID: 76, PPID: 1, Exe: "/bin/bash"},
		70: {PID: 70, PPID: 69, Exe: "/bin/sh"},
		69: {PID: 69, PPID: 1, Exe: "/opt/some-agent"},
	}
	var mu sync.Mutex
	var traffic []ProviderTraffic
	o := New(Options{
		ConfigDir: dir, Keys: keys, Table: table, Processes: procs,
		Resolver:     fakeResolver{"api.openai.com": {openai}},
		Hooks:        []ProviderTrafficHook{func(_ context.Context, p ProviderTraffic) { mu.Lock(); traffic = append(traffic, p); mu.Unlock() }},
		pollInterval: 50 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- o.Run(ctx) }()
	address := Address(dir)
	waitFor(t, "the socket", func() bool { _, err := os.Stat(address); return err == nil })
	if info, _ := os.Stat(address); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", info.Mode().Perm())
	}
	if info, _ := os.Stat(filepath.Dir(address)); info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode %v", info.Mode().Perm())
	}

	secret := "sk-test-SECRET-PROMPT-1234"
	send := func(harness, event string, ppid int, payload string) {
		sctx, scancel := context.WithTimeout(context.Background(), time.Second)
		defer scancel()
		if err := Send(sctx, address, Message{Harness: harness, Event: event, PID: 9999, PPID: ppid, Payload: EventPayload([]byte(payload))}); err != nil {
			t.Fatal(err)
		}
	}
	send("claude", "UserPromptSubmit", 70, `{"prompt":"`+secret+`"}`)
	send("claude", "Stop", 70, `not json`)
	// A second listener on the same address is refused.
	if _, err := listen(address); err == nil {
		t.Fatal("a second observer listened on the same address")
	}

	// A connection from the claude session (hook parent 70, through a shell to 69), and one
	// from a codex process recognized by its script.
	table.set(
		Conn{Local: netip.MustParseAddrPort("10.0.0.2:50000"), Remote: netip.AddrPortFrom(openai, 443), State: "established", PID: 70},
		Conn{Local: netip.MustParseAddrPort("10.0.0.2:50001"), Remote: netip.AddrPortFrom(openai, 443), State: "established", PID: 42},
		Conn{Local: netip.MustParseAddrPort("10.0.0.2:50002"), Remote: netip.MustParseAddrPort("198.51.100.1:443"), State: "established", PID: 77},
	)
	waitFor(t, "two detections", func() bool { mu.Lock(); defer mu.Unlock(); return len(traffic) >= 2 })
	table.set()
	waitFor(t, "close detections", func() bool { mu.Lock(); defer mu.Unlock(); return len(traffic) >= 4 })

	qctx, qcancel := context.WithTimeout(context.Background(), time.Second)
	stats, err := Query(qctx, address)
	qcancel()
	if err != nil || stats.Received != 2 || stats.Detections != 4 || stats.OpenConnections != 0 || !stats.Capture.Available || stats.SocketTable != "ok" {
		t.Fatalf("stats %+v %v", stats, err)
	}
	if stats.RSSBytes == 0 || stats.PeakRSSBytes < stats.RSSBytes {
		t.Fatalf("rss %d peak %d", stats.RSSBytes, stats.PeakRSSBytes)
	}
	mu.Lock()
	harnesses := map[int]string{}
	for _, tr := range traffic {
		if tr.Provider != "openai" || tr.Host != "api.openai.com" || tr.Source != "socket" {
			t.Fatalf("traffic %+v", tr)
		}
		harnesses[tr.PID] = tr.Harness
	}
	mu.Unlock()
	if harnesses[70] != "claude" || harnesses[42] != "codex" {
		t.Fatalf("attribution %v", harnesses)
	}

	waitFor(t, "stored records", func() bool { return o.stored.Load() >= 6 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(address); !os.IsNotExist(err) {
		t.Fatal("socket left behind")
	}

	// Nothing captured is readable on disk; the stored key decrypts every record.
	segments, _ := Segments(CaptureDirectory(dir))
	for _, s := range segments {
		data, _ := os.ReadFile(filepath.Join(CaptureDirectory(dir), s.Name))
		if bytes.Contains(data, []byte(secret)) || bytes.Contains(data, []byte("api.openai.com")) {
			t.Fatal("plaintext in the capture")
		}
	}
	key, err := LoadKey(keys, false)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var prompt bool
	if _, err := ReadCapture(CaptureDirectory(dir), key, func(r []byte) error {
		var rec map[string]any
		_ = json.Unmarshal(r, &rec)
		kinds = append(kinds, rec["kind"].(string)+":"+stringOf(rec["event"])+stringOf(rec["phase"]))
		prompt = prompt || strings.Contains(string(r), secret)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(kinds)
	if strings.Join(kinds, " ") != "connection:close connection:close connection:open connection:open hook:Stop hook:UserPromptSubmit" || !prompt {
		t.Fatalf("records %v", kinds)
	}
	other := bytes.Repeat([]byte{7}, 32)
	if skipped, _ := ReadCapture(CaptureDirectory(dir), other, func([]byte) error { t.Fatal("decrypted with another key"); return nil }); skipped != len(segments) {
		t.Fatalf("skipped %d of %d", skipped, len(segments))
	}

	// Purge through a running observer closes its segment first.
	ctx, cancel = context.WithCancel(context.Background())
	go func() { done <- o.Run(ctx) }()
	waitFor(t, "the socket", func() bool { _, err := os.Stat(address); return err == nil })
	pctx, pcancel := context.WithTimeout(context.Background(), time.Second)
	result, err := RequestPurge(pctx, address)
	pcancel()
	if err != nil || result.Deleted == 0 || result.Error != "" {
		t.Fatalf("purge %+v %v", result, err)
	}
	if left, _ := Segments(CaptureDirectory(dir)); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
	cancel()
	<-done
}

func stringOf(v any) string { s, _ := v.(string); return s }

// Without the credential store there is no capture (and no plaintext fallback); events are
// counted as dropped and the observer keeps serving.
func TestObserverWithoutCredentialStore(t *testing.T) {
	dir := shortDir(t)
	o := New(Options{ConfigDir: dir, Keys: &memoryKeys{fail: true}, Table: &fakeTable{}, Processes: fakeProcs{}, Resolver: fakeResolver{}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go o.Run(ctx)
	address := Address(dir)
	waitFor(t, "the socket", func() bool { _, err := os.Stat(address); return err == nil })
	sctx, scancel := context.WithTimeout(context.Background(), time.Second)
	defer scancel()
	if err := Send(sctx, address, Message{Harness: "claude", Event: "Stop", Payload: EventPayload([]byte(`{}`))}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a dropped record", func() bool { return o.dropped.Load() == 1 })
	stats, err := Query(sctx, address)
	if err != nil || stats.Capture.Available || !strings.Contains(stats.Capture.Detail, "credential store unavailable") {
		t.Fatalf("%+v %v", stats.Capture, err)
	}
	if segments, _ := Segments(CaptureDirectory(dir)); len(segments) != 0 {
		t.Fatal("wrote a capture without a key")
	}
}

// Hooks give up quickly when nothing listens.
func TestSendWithoutObserver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Send(ctx, Address(shortDir(t)), Message{Event: "x"}); err == nil || time.Since(start) > time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}
