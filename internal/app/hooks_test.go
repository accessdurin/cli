package app

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
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
				if n, err := InstallSkills(agent); err != nil || n != 5 {
					t.Fatalf("skills: %d %v", n, err)
				}
			}
			target := agent.HookConfig.Path
			if format != "opencode" {
				writeFile(t, target, `{ // preserve this comment
"theme":"keep","hooks":{}}`)
			}
			if err := InstallHooks(agent, f.bridge, f.checker); err != nil {
				t.Fatal(err)
			}
			saved := readFile(t, target)
			if err := InstallHooks(agent, f.bridge, f.checker); err != nil || readFile(t, target) != saved {
				t.Fatalf("not idempotent: %v", err)
			}
			markers := []string{"preserve this comment", `"keep"`}
			if format == "opencode" {
				markers = []string{"Managed by Durin CLI"}
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

func TestCheckerOutcomesBlockTheToolCall(t *testing.T) {
	f := newHookFixture(t)
	if err := InstallHooks(AgentsFor(f.home, EnvMap{})["claude"], f.bridge, contracts.HookChecker{Command: f.checker.Command, Args: []string{}, TimeoutMs: 100}); err != nil {
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
	input := HookConfiguration{Format: "claude", Bridge: bridge, Harness: "claude", TimeoutMs: 1000}
	raw := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":/* keep inner handler comment */"existing-check"}]}],"PostToolUse":[{"matcher":".*","hooks":[]}]}}`
	rendered, err := UpdatedHookConfig(raw, input)
	if err != nil || !strings.Contains(rendered, "keep inner handler comment") || !strings.Contains(rendered, `"existing-check"`) {
		t.Fatalf("third-party handler lost: %v\n%s", err, rendered)
	}
	config, _ := ParseConfig(rendered, "jsonc")
	hooks, _ := config.GetObject("hooks")
	pre, _ := hooks.GetArray("PreToolUse")
	post, _ := hooks.GetArray("PostToolUse")
	if len(pre) != 2 || jsjson.Stringify(post) != `[{"matcher":".*","hooks":[]}]` {
		t.Fatalf("groups: %s", rendered)
	}
	added := pre[1].(*jsjson.Object)
	handlers, _ := added.GetArray("hooks")
	want := `{"type":"command","command":"'/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'claude' '--config-dir' '/home/u/.config/durin'","timeout":4}`
	if runtime.GOOS == "windows" || jsjson.Stringify(handlers[0]) != want {
		t.Fatalf("handler %s", jsjson.Stringify(handlers[0]))
	}
	for _, invalid := range []string{`{"disableAllHooks":true}`, `{"hooksConfig":{"enabled":false}}`, `{"hooks":{"PreToolUse":{}}}`, `{`} {
		if _, err := UpdatedHookConfig(invalid, input); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}

	windows := true
	input.Windows = &windows
	rendered, _ = UpdatedHookConfig("{}", input)
	if !strings.Contains(rendered, `& '/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'claude' '--config-dir' '/home/u/.config/durin'; exit $LASTEXITCODE`) {
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
	if got := string(utf16.Decode(units)); got != `& '/opt/durin/runtime/durin' 'hooks' 'run' '--harness' 'codex' '--config-dir' '/home/u/.config/durin'; exit $LASTEXITCODE` {
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
	if err := InstallHooks(agent, f.bridge, f.checker); err != nil {
		t.Fatal(err)
	}
	saved := readFile(t, agent.HookConfig.Path)
	if strings.Contains(saved, "cli.mjs") || !strings.Contains(saved, "// user settings") || strings.Count(saved, `"command"`) != 2 || !strings.Contains(saved, f.bridge.ExecutablePath) {
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
		if err := InstallHooks(agent, f.bridge, f.checker); err != nil {
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
	if err := InstallHooks(agent, f.bridge, f.checker); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		manifest, target string
		install          func() error
	}{
		"skills": {filepath.Join(agent.SkillsDirectory, "durin-hooks", ".durin-install.json"), filepath.Join(agent.SkillsDirectory, "durin-hooks", "SKILL.md"), func() error { _, err := InstallSkills(agent); return err }},
		"hooks":  {filepath.Join(f.bridge.ConfigDirectory, "hooks", "claude.json"), agent.HookConfig.Path, func() error { return InstallHooks(agent, f.bridge, f.checker) }},
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
	wantCliError(t, InstallHooks(opencode, f.bridge, f.checker), "Preserving")
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
