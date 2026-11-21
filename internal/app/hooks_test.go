package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/observer"
)

type hookFixture struct {
	home, capture string
	checker       contracts.HookChecker
	bridge        BridgeConfiguration
}

// newHookFixture makes a checker script that records its stdin and exits with tool_input.code
// (or sleeps when tool_input.wait is set).
func newHookFixture(t *testing.T) hookFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script checkers")
	}
	home := tempDir(t)
	capture := filepath.Join(home, "captured.json")
	script := filepath.Join(home, "checker.sh")
	writeFile(t, script, "#!/bin/sh\ncat > '"+capture+"'\ngrep -q '\"wait\":true' '"+capture+"' && exec sleep 2\n"+
		"grep -q '\"code\":2' '"+capture+"' && exit 2\ngrep -q '\"code\":1' '"+capture+"' && exit 1\nexit 0\n")
	_ = os.Chmod(script, 0o700)
	return hookFixture{
		home:    home,
		capture: capture,
		checker: contracts.HookChecker{Command: script, Args: []string{}, TimeoutMs: 1000},
		bridge:  BridgeConfiguration{ExecutablePath: filepath.Join(home, "runtime", "durin"), ConfigDirectory: filepath.Join(home, "durin"), ProfileID: testProfileID},
	}
}

var canonicalEvent = `{"hook_event_name":"PreToolUse","session_id":"session","tool_use_id":"call","tool_name":"mcp__sandbox__read","tool_input":{"resource":"handbook"}}`

func hookInputFor(format string) string {
	switch format {
	case "windsurf":
		return `{"agent_action_name":"pre_mcp_tool_use","session_id":"session","tool_use_id":"call","tool_info":{"mcp_server_name":"sandbox","mcp_tool_name":"read","mcp_tool_arguments":{"resource":"handbook"}}}`
	case "copilot":
		return `{"sessionId":"session","toolCallId":"call","toolName":"mcp__sandbox__read","toolArgs":"{\"resource\":\"handbook\"}"}`
	}
	return canonicalEvent
}

func TestInstallHooksForEveryFormatAndRunARealChecker(t *testing.T) {
	for _, format := range HookFormats {
		t.Run(format, func(t *testing.T) {
			f := newHookFixture(t)
			agent := AgentsFor(f.home, EnvMap{})[format]
			if format == "custom" {
				agent = AgentsFor(f.home, EnvMap{})["claude"]
				agent.ID, agent.SkillsDirectory = "custom", filepath.Join(f.home, "custom-skills")
				agent.HookConfig = &contracts.HookConfig{Path: filepath.Join(f.home, "custom-hooks.json"), Format: "custom"}
			}
			for range 2 {
				want := 5
				if agent.SkillsDirectory == "" {
					want = 0
				}
				if n, err := InstallSkills(agent); err != nil || n != want {
					t.Fatalf("skills: %d %v", n, err)
				}
			}
			target := agent.HookConfig.Path
			managed := managedHookFormats[format]
			if !managed {
				writeFile(t, target, `{ // preserve this comment
"theme":"keep","hooks":{}}`)
			}
			if err := InstallHooks(agent, f.bridge, &f.checker); err != nil {
				t.Fatal(err)
			}
			saved := readFile(t, target)
			if err := InstallHooks(agent, f.bridge, &f.checker); err != nil || readFile(t, target) != saved {
				t.Fatalf("not idempotent: %v", err)
			}
			markers := []string{"preserve this comment", `"keep"`}
			if managed {
				markers = []string{"Managed by Durin CLI"}
			}
			// Every event the harness supports has a durin handler.
			for _, event := range registeredEvents(format, true) {
				if !strings.Contains(saved, event.Name) {
					t.Errorf("no handler for %s", event.Name)
				}
			}
			for _, marker := range markers {
				if !strings.Contains(saved, marker) {
					t.Fatalf("lost %q:\n%s", marker, saved)
				}
			}
			installation, err := ReadHookInstallation(f.bridge.ConfigDirectory, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := RunHook(installation, strings.NewReader(hookInputFor(format))); err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSuffix(canonicalEvent, "}") + `,"durin_harness":"` + agent.ID + `"}`
			if got := readFile(t, f.capture); got != want {
				t.Fatalf("checker received %s\nwant %s", got, want)
			}
		})
	}
}

// Without a checker every event, the pre-tool one included, only captures; Cursor's
// permission hook, which needs an answer, is not registered at all. A later run without a
// checker keeps the checker an earlier run configured.
func TestCaptureOnlyHooks(t *testing.T) {
	f := newHookFixture(t)
	agents := AgentsFor(f.home, EnvMap{})
	for _, format := range []string{"cursor", "claude", "kiro"} {
		agent := agents[format]
		if err := InstallHooks(agent, f.bridge, nil); err != nil {
			t.Fatal(err)
		}
		saved := readFile(t, agent.HookConfig.Path)
		installation, err := ReadHookInstallation(f.bridge.ConfigDirectory, format)
		if err != nil || installation.Checker != nil || strings.Contains(saved, "failClosed") || strings.Contains(saved, "durin-local-check") {
			t.Fatalf("%s: %+v %v\n%s", format, installation, err, saved)
		}
		if format == "cursor" && (strings.Contains(saved, `"preToolUse"`) || !strings.Contains(saved, `"beforeSubmitPrompt"`)) {
			t.Fatalf("cursor events:\n%s", saved)
		}
		if format != "kiro" && installation.Handler != nil {
			t.Fatalf("%s: a check handler was recorded without a checker", format)
		}
	}
	if err := InstallHooks(agents["claude"], f.bridge, &f.checker); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(agents["claude"], f.bridge, nil); err != nil {
		t.Fatal(err)
	}
	if installation, _ := ReadHookInstallation(f.bridge.ConfigDirectory, "claude"); installation.Checker == nil || installation.Handler == nil {
		t.Fatal("the earlier checker was dropped")
	}
	codex := agents["codex"]
	writeFile(t, codex.ConfigPath, "[features]\nhooks = false\n")
	wantCliError(t, InstallHooks(codex, f.bridge, nil), "Hooks are disabled")
}

func TestCheckerOutcomesBlockTheToolCall(t *testing.T) {
	f := newHookFixture(t)
	if err := InstallHooks(AgentsFor(f.home, EnvMap{})["claude"], f.bridge, &contracts.HookChecker{Command: f.checker.Command, Args: []string{}, TimeoutMs: 1000}); err != nil {
		t.Fatal(err)
	}
	installation, _ := ReadHookInstallation(f.bridge.ConfigDirectory, "claude")
	event := func(input string) string { return strings.Replace(canonicalEvent, `{"resource":"handbook"}`, input, 1) }
	for input, message := range map[string]string{`{"code":2}`: "denied", `{"code":1}`: "failed", `{"wait":true}`: "timed out"} {
		wantCliError(t, RunHook(installation, strings.NewReader(event(input))), message)
	}
	_ = os.Remove(f.capture)
	for _, input := range []string{`{}`, event(`[1]`)} {
		wantCliError(t, RunHook(installation, strings.NewReader(input)), "Invalid local hook input")
	}
	wantCliError(t, RunHook(installation, strings.NewReader(strings.Repeat("x", 1_048_577))), "exceeds 1 MiB")
	if _, err := os.Stat(f.capture); !os.IsNotExist(err) {
		t.Fatal("the checker ran on invalid input")
	}
}

func TestHookConfigEdits(t *testing.T) {
	bridge := BridgeConfiguration{ExecutablePath: "/opt/durin/runtime/durin", ConfigDirectory: "/home/u/.config/durin", ProfileID: testProfileID}
	posix := false
	input := HookConfiguration{Format: "claude", Bridge: bridge, Harness: "claude", TimeoutMs: 1000, Checker: true, Windows: &posix}
	raw := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":/* keep inner handler comment */"existing-check"}]}],"PostToolUse":[{"matcher":".*","hooks":[]}]}}`
	rendered, err := UpdatedHookConfig(raw, input)
	if err != nil || !strings.Contains(rendered, "keep inner handler comment") || !strings.Contains(rendered, `"existing-check"`) {
		t.Fatalf("third-party handler lost: %v\n%s", err, rendered)
	}
	config, _ := ParseConfig(rendered, "jsonc")
	hooks, _ := config.GetObject("hooks")
	pre, _ := hooks.GetArray("PreToolUse")
	post, _ := hooks.GetArray("PostToolUse")
	if len(pre) != 2 || len(post) != 2 || jsjson.Stringify(post[0]) != `{"matcher":".*","hooks":[]}` {
		t.Fatalf("groups: %s", rendered)
	}
	added := pre[1].(*jsjson.Object)
	handlers, _ := added.GetArray("hooks")
	want := `{"type":"command","command":"'/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'claude' '--config-dir' '/home/u/.config/durin' '--event' 'PreToolUse'","timeout":4}`
	if jsjson.Stringify(handlers[0]) != want {
		t.Fatalf("handler %s", jsjson.Stringify(handlers[0]))
	}
	// Capture handlers: a short timeout, and no matcher outside tool events.
	stop, _ := hooks.GetArray("Stop")
	if got := jsjson.Stringify(stop); got != `[{"hooks":[{"type":"command","command":"'/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'claude' '--config-dir' '/home/u/.config/durin' '--event' 'Stop'","timeout":10}]}]` {
		t.Fatalf("Stop: %s", got)
	}
	for _, excluded := range []string{"WorktreeCreate", "WorktreeRemove", "PreModelSwitch", "MessageDisplay"} {
		if _, ok := hooks.Get(excluded); ok {
			t.Errorf("registered %s", excluded)
		}
	}
	for _, invalid := range []string{`{"disableAllHooks":true}`, `{"hooksConfig":{"enabled":false}}`, `{"hooks":{"PreToolUse":{}}}`, `{`} {
		if _, err := UpdatedHookConfig(invalid, input); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}

	windows := true
	input.Windows = &windows
	rendered, _ = UpdatedHookConfig("{}", input)
	if !strings.Contains(rendered, `& '/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'claude' '--config-dir' '/home/u/.config/durin' '--event' 'PreToolUse'; exit $LASTEXITCODE`) || !strings.Contains(rendered, `"shell": "powershell"`) {
		t.Fatalf("claude on Windows:\n%s", rendered)
	}
	input.Format, input.Harness = "codex", "codex"
	rendered, _ = UpdatedHookConfig("{}", input)
	encoded := regexp.MustCompile(`-EncodedCommand ([A-Za-z0-9+/=]+)`).FindStringSubmatch(rendered)[1]
	data, _ := base64.StdEncoding.DecodeString(encoded)
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	if got := string(utf16.Decode(units)); got != `& '/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'codex' '--config-dir' '/home/u/.config/durin' '--event' 'PreToolUse'; exit $LASTEXITCODE` {
		t.Fatalf("codex on Windows runs %q", got)
	}
	if !strings.Contains(rendered, `"command": "powershell.exe -NoProfile -NonInteractive -EncodedCommand `) {
		t.Fatalf("codex on Windows:\n%s", rendered)
	}
}

// A handler the TypeScript CLI installed (node + runtime/cli.mjs) is replaced in place.
func TestHooksReplaceTheTypeScriptHandler(t *testing.T) {
	f := newHookFixture(t)
	agent := AgentsFor(f.home, EnvMap{})["claude"]
	legacyCommand := strings.Join([]string{"'/usr/local/bin/node'", "'" + legacyRuntime(f.bridge.ConfigDirectory) + "'", "'hooks'", "'run'", "'--harness'", "'claude'", "'--config-dir'", "'" + f.bridge.ConfigDirectory + "'"}, " ")
	handler := jsjson.Obj(jsjson.KV("type", jsjson.String("command")), jsjson.KV("command", jsjson.String(legacyCommand)), jsjson.KV("timeout", jsjson.Number(4)))
	settings := "{\n  // user settings\n  \"hooks\": {\n    \"PreToolUse\": [\n      {\n        \"matcher\": \".*\",\n        \"hooks\": [\n          " + jsjson.Stringify(handler) + "\n        ]\n      }\n    ]\n  }\n}\n"
	writeFile(t, agent.HookConfig.Path, settings)
	record := jsjson.Obj(
		jsjson.KV("format", jsjson.String("claude")), jsjson.KV("harness", jsjson.String("claude")),
		jsjson.KV("checker", CheckerJSON(f.checker)), jsjson.KV("target", jsjson.String(agent.HookConfig.Path)),
		jsjson.KV("installedAt", jsjson.String("2026-01-01T00:00:00.000Z")), jsjson.KV("handler", handler),
		jsjson.KV("assetDigest", jsjson.String(textDigest(settings))),
	)
	writeFile(t, filepath.Join(f.bridge.ConfigDirectory, "hooks", "claude.json"), jsjson.StringifyIndent(record, 2)+"\n")
	if err := InstallHooks(agent, f.bridge, &f.checker); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, agent.HookConfig.Path)
	config, _ := ParseConfig(saved, "jsonc")
	hooks, _ := config.GetObject("hooks")
	pre, _ := hooks.GetArray("PreToolUse")
	if strings.Contains(saved, "cli.mjs") || !strings.Contains(saved, "// user settings") || len(pre) != 1 || !strings.Contains(saved, f.bridge.ExecutablePath) {
		t.Fatalf("legacy handler not replaced:\n%s", saved)
	}

	custom := HookConfiguration{Format: "custom", Bridge: f.bridge, Harness: "custom", TimeoutMs: 1000}
	legacy := `{"durin":{"command":["/usr/local/bin/node",` + jsjson.Quote(legacyRuntime(f.bridge.ConfigDirectory)) + `,"hooks","run","--harness","custom","--config-dir",` + jsjson.Quote(f.bridge.ConfigDirectory) + `]}}`
	rendered, err := UpdatedHookConfig(legacy, custom)
	if err != nil || strings.Contains(rendered, "cli.mjs") {
		t.Fatalf("legacy custom descriptor not replaced: %v %s", err, rendered)
	}
	_, err = UpdatedHookConfig(`{"durin":{"command":["/usr/local/bin/node","/elsewhere/cli.mjs"]}}`, custom)
	wantCliError(t, err, "Conflicting custom Durin hook configuration")
}

func TestShellHookCommandIsQuoted(t *testing.T) {
	f := newHookFixture(t)
	special := filepath.Join(f.home, "agent's $(touch injected)")
	f.bridge.ExecutablePath = filepath.Join(special, "durin")
	f.bridge.ConfigDirectory = filepath.Join(special, "config")
	writeFile(t, f.bridge.ExecutablePath, "#!/bin/sh\ncat > '"+f.capture+"'\n[ \"$1 $2\" = 'hooks run' ] || exit 1\ngrep -q '\"code\":2' '"+f.capture+"' && exit 2\nexit 0\n")
	_ = os.Chmod(f.bridge.ExecutablePath, 0o700)
	for _, format := range []string{"claude", "codex"} {
		agent := AgentsFor(f.home, EnvMap{})[format]
		if err := InstallHooks(agent, f.bridge, &f.checker); err != nil {
			t.Fatal(err)
		}
		config, _ := ParseConfig(readFile(t, agent.HookConfig.Path), "jsonc")
		hooks, _ := config.GetObject("hooks")
		groups, _ := hooks.GetArray("PreToolUse")
		handlers, _ := groups[0].(*jsjson.Object).GetArray("hooks")
		command, _ := handlers[0].(*jsjson.Object).GetString("command")
		for code, want := range map[string]int{"0": 0, "2": 2} {
			cmd := exec.Command("/bin/sh", "-c", command)
			cmd.Dir = f.home
			cmd.Stdin = bytes.NewBufferString(`{"tool_input":{"code":` + code + `}}`)
			err := cmd.Run()
			if got := cmd.ProcessState.ExitCode(); got != want {
				t.Fatalf("%s exit %d (%v), want %d", format, got, err, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(f.home, "injected")); !os.IsNotExist(err) {
		t.Fatal("the hook command ran an injected command")
	}
}

func TestInterruptedSaveKeepsTheManifest(t *testing.T) {
	f := newHookFixture(t)
	agent := AgentsFor(f.home, EnvMap{})["claude"]
	if _, err := InstallSkills(agent); err != nil {
		t.Fatal(err)
	}
	if err := InstallHooks(agent, f.bridge, &f.checker); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		manifest, target string
		install          func() error
	}{
		"skills": {filepath.Join(agent.SkillsDirectory, "durin-hooks", ".durin-install.json"), filepath.Join(agent.SkillsDirectory, "durin-hooks", "SKILL.md"), func() error { _, err := InstallSkills(agent); return err }},
		"hooks":  {filepath.Join(f.bridge.ConfigDirectory, "hooks", "claude.json"), agent.HookConfig.Path, func() error { return InstallHooks(agent, f.bridge, &f.checker) }},
	}
	defer func() { saveFileChange = SaveFileChange }()
	for name, c := range cases {
		before := readFile(t, c.manifest)
		var attempted []string
		saveFileChange = func(change FileChange) error {
			attempted = append(attempted, change.Path)
			if change.Path == c.target {
				return os.ErrPermission
			}
			return SaveFileChange(change)
		}
		if err := c.install(); err != os.ErrPermission {
			t.Fatalf("%s: %v", name, err)
		}
		for _, path := range attempted {
			if path == c.manifest {
				t.Fatalf("%s: manifest saved after a failed artifact", name)
			}
		}
		if readFile(t, c.manifest) != before {
			t.Fatalf("%s: manifest changed", name)
		}
	}
	saveFileChange = SaveFileChange
	// An edited OpenCode plugin is preserved.
	opencode := AgentsFor(f.home, EnvMap{})["opencode"]
	writeFile(t, opencode.HookConfig.Path, "// user plugin")
	wantCliError(t, InstallHooks(opencode, f.bridge, &f.checker), "Preserving")
}

func TestCustomHarnessDescriptor(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "custom.json")
	descriptor := func(configPath string) string {
		return `{"id":"custom-test","name":"My harness","configPath":` + jsjson.Quote(configPath) +
			`,"format":"jsonc","serverKey":"mcpServers","bridge":"arguments","skillsDirectory":` + jsjson.Quote(filepath.Join(dir, "skills")) +
			`,"hookConfig":{"path":` + jsjson.Quote(filepath.Join(dir, "hooks.json")) + `,"format":"custom"}}`
	}
	writeFile(t, path, descriptor(filepath.Join(dir, "agent.json")))
	agent, err := ReadCustomHarness(path)
	if err != nil || agent.ID != "custom-test" || agent.URLKey != "url" || agent.BridgeTransport.Len() != 0 || agent.HookConfig.Format != "custom" {
		t.Fatalf("%+v %v", agent, err)
	}
	for _, bad := range []string{descriptor("../relative.json"), strings.Replace(descriptor(filepath.Join(dir, "a")), `"custom-test"`, `"other"`, 1), strings.Replace(descriptor(filepath.Join(dir, "a")), `}}`, `},"extra":1}`, 1)} {
		writeFile(t, path, bad)
		_, err := ReadCustomHarness(path)
		wantCliError(t, err, "Invalid custom harness")
	}
}

// `hooks run --event` forwards every payload to the observer; capture events print the
// harness's continue answer, the pre-tool event still runs the checker with its exit codes,
// and capture events never fail even without an installation or an observer.
func TestHookEventsForwardAndContinue(t *testing.T) {
	f := newHookFixture(t)
	dir, err := os.MkdirTemp("", "hk") // short: Unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	f.bridge.ConfigDirectory = dir
	agents := AgentsFor(f.home, EnvMap{})
	for _, format := range []string{"claude", "cursor"} {
		if err := InstallHooks(agents[format], f.bridge, &f.checker); err != nil {
			t.Fatal(err)
		}
	}
	address := observer.Address(dir)
	_ = os.MkdirAll(filepath.Dir(address), 0o700)
	l, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	received := make(chan observer.Message, 10)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			data, _ := observer.ReadFrame(conn)
			conn.Close()
			var m observer.Message
			_ = json.Unmarshal(data, &m)
			received <- m
		}
	}()
	run := func(harness, event, input string) (string, error) {
		var out bytes.Buffer
		err := RunHookEvent(&CliOptions{Directory: dir}, &IO{Stdin: strings.NewReader(input), Stdout: &out}, harness, event)
		return out.String(), err
	}
	for _, c := range []struct{ harness, event, input, output string }{
		{"claude", "UserPromptSubmit", `{"prompt":"hi"}`, ""},
		{"cursor", "beforeSubmitPrompt", `{"prompt":"hi"}`, `{"continue":true}` + "\n"},
		{"cursor", "afterFileEdit", `not json`, "{}\n"},
		{"cursor", "preToolUse", `{"conversation_id":"c","tool_use_id":"t","tool_name":"x","tool_input":{}}`, `{"permission":"allow"}` + "\n"},
		{"claude", "PreToolUse", canonicalEvent, ""},
	} {
		out, err := run(c.harness, c.event, c.input)
		if err != nil || out != c.output {
			t.Fatalf("%s %s: %q %v", c.harness, c.event, out, err)
		}
		select {
		case m := <-received:
			if m.Type != observer.TypeEvent || m.Harness != c.harness || m.Event != c.event || m.PID != os.Getpid() || !strings.Contains(string(m.Payload), strings.Trim(c.input, "{}")[:5]) {
				t.Fatalf("forwarded %+v", m)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s %s was not forwarded", c.harness, c.event)
		}
	}
	// The checker's denial and invalid input keep their errors on the pre-tool event.
	_, err = run("claude", "PreToolUse", strings.Replace(canonicalEvent, `{"resource":"handbook"}`, `{"code":2}`, 1))
	wantCliError(t, err, "denied")
	if _, err = run("cursor", "preToolUse", `[]`); err == nil {
		t.Fatal("accepted a non-object pre-tool event")
	}
	_, err = run("codex", "PreToolUse", canonicalEvent) // no installation: fail closed
	wantCliError(t, err, "No local checker configured")
	l.Close()
	if out, err := run("codex", "Stop", `{}`); err != nil || out != "" {
		t.Fatalf("capture without installation or observer: %q %v", out, err)
	}
	for args, capture := range map[string]bool{
		"hooks run --harness claude --event Stop":           true,
		"hooks run --harness claude --event=PreToolUse":     false,
		"hooks run --harness cursor --event preToolUse":     false,
		"hooks run --harness opencode --event session.idle": true,
		"hooks run --harness claude":                        false,
		"hooks status --event Stop":                         false,
		// Decided by event names alone, every --event value counted.
		"hooks run --harness custom-acme --event PreToolUse":               false,
		"hooks run --harness claude --event Stop --event PreToolUse":       false,
		"hooks run --harness claude --event=PreToolUse --event Stop":       false,
		"hooks run --harness claude --event Stop --event SessionEnd":       true,
		"hooks run --harness claude --app-url http://x --event Stop":       true,
		"hooks run --harness claude --app-url http://x --event PreToolUse": false,
		"hooks run --nope --event PreToolUse":                              false,
		"hooks run --harness cursor --event preToolUse --nope":             false,
		"hooks run --harness pi --event tool_call":                         false,
		"hooks run --harness claude --event":                               false,
		"hooks run --event --event PreToolUse":                             false,
		"hooks run --event --event=PreToolUse":                             false,
		"hooks run --event=":                                               false,
	} {
		if got := IsCaptureHook(strings.Fields(args)); got != capture {
			t.Errorf("%s: %v", args, got)
		}
	}
}

func TestWindsurfConversationAndTurnIDs(t *testing.T) {
	normalize := func(input string) *jsjson.Object {
		t.Helper()
		raw, err := jsjson.Parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		out, err := NormalizeHook(raw, "windsurf")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	const tool = `"agent_action_name":"pre_mcp_tool_use","tool_info":{"mcp_server_name":"sandbox","mcp_tool_name":"read","mcp_tool_arguments":{}}`
	mapped := normalize(`{` + tool + `,"trajectory_id":"conversation","execution_id":"turn"}`)
	if s, _ := mapped.GetString("session_id"); s != "conversation" {
		t.Fatalf("session_id = %q, want the trajectory_id", s)
	}
	if s, _ := mapped.GetString("turn_id"); s != "turn" {
		t.Fatalf("turn_id = %q, want the execution_id", s)
	}
	native := normalize(`{` + tool + `,"session_id":"native","trajectory_id":"other"}`)
	if s, _ := native.GetString("session_id"); s != "native" {
		t.Fatalf("session_id = %q, want the native session_id", s)
	}
	uuid := regexp.MustCompile(`^[0-9a-f-]{36}$`)
	anonymous := `{` + tool + `,"trajectory_id":"","execution_id":""}`
	first, second := normalize(anonymous), normalize(anonymous)
	firstID, _ := first.GetString("session_id")
	secondID, _ := second.GetString("session_id")
	if !uuid.MatchString(firstID) || firstID == secondID {
		t.Fatalf("calls without a conversation ID must get distinct opaque IDs: %q %q", firstID, secondID)
	}
	if _, ok := first.Get("turn_id"); ok {
		t.Fatal("an empty execution_id must not become a turn_id")
	}
}
