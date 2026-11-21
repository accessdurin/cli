package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeSystemctl stands in for the user's service manager: it logs each call and reports the
// unit active, so setup's observer service step is deterministic.
func (e *Env) fakeSystemctl() string {
	log := filepath.Join(e.Root, "systemctl.log")
	e.Executable("systemctl", `printf '%s\n' "$*" >> "`+log+`"
[ "$2" = is-active ] && echo active
exit 0
`)
	return log
}

var managedLauncher = regexp.MustCompile(`"[^"]*/runtime/durin(\.exe)?"`)

// hookSummary lists, per event, durin's handlers in a JSON hook configuration (matcher and
// handler fields), so a golden shows exactly which events are registered and how.
func hookSummary(path string) string {
	var config struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(ReadFile(path)), &config); err != nil {
		return "unreadable: " + err.Error() + "\n"
	}
	var events []string
	for event := range config.Hooks {
		events = append(events, event)
	}
	sort.Strings(events)
	var b strings.Builder
	for _, event := range events {
		for _, entry := range config.Hooks[event] {
			handlers := []any{entry}
			if nested, ok := entry["hooks"].([]any); ok {
				handlers = nested
			}
			for _, h := range handlers {
				data, _ := json.Marshal(h)
				if !strings.Contains(string(data), "hooks") || !strings.Contains(string(data), "run") {
					continue
				}
				matcher := ""
				if m, ok := entry["matcher"].(string); ok {
					matcher = " matcher=" + m
				}
				fmt.Fprintf(&b, "%s%s %s\n", event, matcher, normalizeLaunchers(strings.ReplaceAll(string(data), `&`, "&")))
			}
		}
	}
	return managedLauncher.ReplaceAllString(b.String(), `"<LAUNCHER>"`)
}

// TestHookInstallAllHarnesses sets up every built-in harness through interactive setup
// without a checker: each gets a capture handler for every hook event it supports (Cursor's
// permission hooks excepted), and setup installs the observer service.
func TestHookInstallAllHarnesses(t *testing.T) {
	e := NewEnv(t)
	requireKeyring(t)
	e.Server.Pages = [][]Tool{{{"name": "github.create_issue", "inputSchema": map[string]any{"type": "object"}}}}
	harnesses := []string{"claude", "codex", "cursor", "windsurf", "gemini", "copilot", "opencode", "pi", "kiro"}
	for _, h := range harnesses {
		e.Executable(h, "exit 0\n")
	}
	e.browser()
	systemctl := e.fakeSystemctl()
	targets := map[string]string{
		"claude": ".claude/settings.json", "codex": ".codex/hooks.json", "cursor": ".cursor/hooks.json",
		"windsurf": ".codeium/windsurf/hooks.json", "gemini": ".gemini/settings.json", "copilot": ".copilot/hooks/durin.json",
		"opencode": ".config/opencode/plugins/durin.js", "pi": ".pi/agent/extensions/durin.ts", "kiro": ".kiro/hooks/durin.json",
	}
	for i, h := range harnesses {
		term := e.StartTerminal("login", "--dev", "--app-url", e.Server.Origin(), "--config-dir", e.Config)
		term.Expect("Which organization should this agent use?")
		term.Type(keyEnter)
		term.Expect("Which agent would you like to use?")
		term.Type(strings.Repeat(keyDown, i) + keyEnter)
		term.Expect("Configure local pre-tool checks?")
		term.Type(keyDown + keyEnter) // skip: capture-only hooks
		// claude, codex, cursor and windsurf report local MCP tool calls.
		if i < 4 {
			term.Expect("Report local MCP tool calls to your organization?")
			term.Type("y")
		}
		term.Expect("Install the Durin observer?")
		term.Type("y")
		term.Expect("Durin observer: installed as a systemd-user service.")
		term.Expect("Setup complete")
		code := term.Wait()
		e.Record([]string{"login (" + h + ", interactive)"}, Result{Code: code})
		if code != 0 {
			t.Fatalf("%s setup failed:\n%s", h, term.plain())
		}
		target := filepath.Join(e.Home, targets[h])
		switch h {
		case "opencode", "pi", "kiro":
			e.Note(h+" hooks "+targets[h], managedLauncher.ReplaceAllString(normalizeLaunchers(ReadFile(target)), `"<LAUNCHER>"`))
		default:
			e.Note(h+" hooks "+targets[h], hookSummary(target))
		}
	}
	e.Server.ResetLog()
	e.RunSortedList("harness", "hooks", "status", "--config-dir", e.Config)
	e.Note("systemctl calls", ReadFile(systemctl))
	e.noteFile("observer unit", filepath.Join(e.Home, ".config", "systemd", "user", "durin-observer.service"))
}

// startObserver runs `durin observe` for this environment and waits for its socket.
func (e *Env) startObserver(extra ...string) func() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := e.command(ctx, []string{"observe", "--config-dir", e.Config})
	cmd.Env = append(cmd.Env, extra...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	socket := filepath.Join(e.Config, "observer", "observer.sock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			e.t.Fatalf("observer did not start: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cancel()
			e.t.Error("observer did not stop on SIGINT")
		}
		cancel()
	}
}

// statusSummary keeps the stable parts of `durin observer status`.
func statusSummary(stdout string) string {
	var status struct {
		Service  map[string]any `json:"service"`
		Running  bool           `json:"running"`
		Observer *struct {
			Received   int            `json:"eventsReceived"`
			Stored     int            `json:"recordsStored"`
			Dropped    int            `json:"recordsDropped"`
			Detections int            `json:"detections"`
			Socket     string         `json:"socketTable"`
			RSS        int64          `json:"rssBytes"`
			Capture    map[string]any `json:"capture"`
			DNS        []any          `json:"dnsSources"`
		} `json:"observer"`
		Capture map[string]any `json:"capture"`
	}
	if err := json.Unmarshal([]byte(stdout), &status); err != nil {
		return "unparseable: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "running=%v service.installed=%v service.manager=%v\n", status.Running, status.Service["installed"], status.Service["manager"])
	capture := status.Capture
	if o := status.Observer; o != nil {
		fmt.Fprintf(&b, "received=%d stored=%d dropped=%d detections=%d socketTable=%s rssUnder200MB=%v dnsSources=%d\n",
			o.Received, o.Stored, o.Dropped, o.Detections, o.Socket, o.RSS > 0 && o.RSS < 200<<20, len(o.DNS))
		capture = o.Capture
	}
	fmt.Fprintf(&b, "capture: available=%v segments=%v maxBytes=%.0f maxAgeHours=%v\n", capture["available"], capture["segments"], capture["maxBytes"], capture["maxAgeHours"])
	return b.String()
}

// TestObserver runs the observer black-box: hook events reach it over the owner-only
// socket, AI-provider connections are detected in the real socket table against a pinned
// resolution, the capture is encrypted with a key from the OS credential store, and export
// and purge work through the CLI. Hooks keep working when the observer is down.
func TestObserver(t *testing.T) {
	e := NewEnv(t)
	requireKeyring(t)
	e.Run("", "observer", "status", "--config-dir", e.Config)
	// Down: a capture hook continues silently and nothing is stored.
	e.Run(`{"prompt":"while down"}`, "hooks", "run", "--harness", "claude", "--config-dir", e.Config, "--event", "UserPromptSubmit")
	e.Run(`not json`, "hooks", "run", "--harness", "cursor", "--config-dir", e.Config, "--event", "beforeSubmitPrompt")

	// A fake provider endpoint on loopback, pinned as api.anthropic.com's address.
	listener, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("no 127.0.0.2 loopback: " + err.Error())
	}
	defer listener.Close()
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = bufio.NewReader(c).ReadByte(); c.Close() }()
		}
	}()
	e.WriteFile(filepath.Join(e.Config, "observer", "config.json"), `{"pollIntervalSeconds":1,"captureMaxAgeHours":24}`, 0o600)
	stop := e.startObserver("DURIN_OBSERVER_RESOLVE=api.anthropic.com=127.0.0.2")
	socket := filepath.Join(e.Config, "observer", "observer.sock")
	info, _ := os.Stat(socket)
	dir, _ := os.Stat(filepath.Dir(socket))
	e.Note("socket permissions", fmt.Sprintf("socket %o, directory %o\n", info.Mode().Perm(), dir.Mode().Perm()))

	secret := "sk-ant-e2e-SECRET-0123456789"
	e.Run(`{"session_id":"s1","prompt":"use `+secret+`"}`, "hooks", "run", "--harness", "claude", "--config-dir", e.Config, "--event", "UserPromptSubmit")
	e.Run(`{"session_id":"s1","stop_hook_active":false}`, "hooks", "run", "--harness", "codex", "--config-dir", e.Config, "--event", "Stop")
	e.Run(`{"type":"session.idle","properties":{"sessionID":"s2"}}`, "hooks", "run", "--harness", "opencode", "--config-dir", e.Config, "--event", "session.idle")

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	waitStatus := func(want func(string) bool) string {
		deadline := time.Now().Add(15 * time.Second)
		for {
			out := e.runQuiet("", "observer", "status", "--config-dir", e.Config).Stdout
			if want(out) || time.Now().After(deadline) {
				return out
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	waitStatus(func(s string) bool { return strings.Contains(s, `"openProviderConnections": 1`) })
	conn.Close()
	out := waitStatus(func(s string) bool {
		return strings.Contains(s, `"detections": 2`) && strings.Contains(s, `"recordsStored": 5`)
	})
	e.Record([]string{"observer status --config-dir <TMP>/config (summarized)"}, Result{Stdout: statusSummary(out)})

	// Encrypted at rest: neither the prompt nor the provider name is on disk.
	captureDir := filepath.Join(e.Config, "observer", "capture")
	entries, _ := os.ReadDir(captureDir)
	for _, entry := range entries {
		data, _ := os.ReadFile(filepath.Join(captureDir, entry.Name()))
		if strings.Contains(string(data), secret) || strings.Contains(string(data), "anthropic") {
			t.Fatalf("plaintext in %s", entry.Name())
		}
		info, _ := entry.Info()
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", entry.Name(), info.Mode().Perm())
		}
	}
	e.Note("credentials", e.CredentialKeys("observer:"))

	// Export decrypts every record.
	export := e.runQuiet("", "observer", "export", "--config-dir", e.Config)
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(export.Stdout), "\n") {
		var r map[string]any
		if json.Unmarshal([]byte(line), &r) != nil {
			lines = append(lines, "unparseable: "+line)
			continue
		}
		harness := r["harness"]
		if r["kind"] == "connection" {
			// The test process opened it, so it is attributed to whichever harness's hook
			// the test process ran last; hook connections are served concurrently.
			harness = fmt.Sprintf("<one of the hook harnesses: %v>", harness == "claude" || harness == "codex" || harness == "opencode")
		}
		summary := fmt.Sprintf("%v harness=%v %v%v", r["kind"], harness, r["event"], r["phase"])
		if p, ok := r["payload"]; ok {
			data, _ := json.Marshal(p)
			summary += " " + string(data)
		}
		if r["kind"] == "connection" {
			remote, _ := r["remote"].(string)
			summary += fmt.Sprintf(" provider=%v host=%v remote=%v pidIsTest=%v", r["provider"], r["host"], strings.Split(remote, ":")[0], r["pid"] == float64(os.Getpid()))
		}
		lines = append(lines, strings.ReplaceAll(summary, "<nil>", ""))
	}
	sort.Strings(lines) // hook connections are served concurrently
	e.Record([]string{"observer export --config-dir <TMP>/config (summarized, sorted)"}, Result{Code: export.Code, Stdout: strings.Join(lines, "\n") + "\n", Stderr: export.Stderr})

	e.Run("", "observer", "purge", "--config-dir", e.Config)
	out = e.runQuiet("", "observer", "status", "--config-dir", e.Config).Stdout
	e.Record([]string{"observer status after purge (summarized)"}, Result{Stdout: statusSummary(out)})
	stop()
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Error("socket left behind after the observer stopped")
	}
	e.Run("", "observer", "purge", "--config-dir", e.Config)
	e.Run("", "observer", "bogus", "--config-dir", e.Config)
}
