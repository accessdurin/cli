package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checker installs a fake local checker that records its stdin and exits with the code in
// <root>/checker-exit (default 0).
func (e *Env) checker() string {
	return e.Executable(filepath.Join(e.Root, "checker", "check.sh"), `cat > "`+e.Root+`/checker-input"
code=$(cat "`+e.Root+`/checker-exit" 2>/dev/null || echo 0)
[ "$code" = sleep ] && exec sleep 5
exit "$code"
`)
}

func (e *Env) installHook(harness, format, target, command string, timeoutMs int) {
	targetContent := "{}\n"
	e.WriteFile(target, targetContent, 0o600)
	sum := sha256.Sum256([]byte(targetContent))
	installation := map[string]any{
		"format": format, "harness": harness,
		"checker":     map[string]any{"command": command, "args": []string{"--client", harness}, "timeoutMs": timeoutMs},
		"target":      target,
		"installedAt": "2026-01-01T00:00:00.000Z",
		"assetDigest": hex.EncodeToString(sum[:]),
	}
	data, _ := json.MarshalIndent(installation, "", "  ")
	e.WriteFile(filepath.Join(e.Config, "hooks", harness+".json"), string(data)+"\n", 0o600)
}

func (e *Env) runHook(harness, input string) {
	_ = os.Remove(filepath.Join(e.Root, "checker-input"))
	e.Run(input, "hooks", "run", "--harness", harness, "--config-dir", e.Config)
	e.Note("checker received", ReadFile(filepath.Join(e.Root, "checker-input")))
}

func TestHooks(t *testing.T) {
	e := NewEnv(t)
	e.Run("", "hooks", "status", "--config-dir", e.Config)
	e.Note("hooks directory", e.Tree(e.Config))
	e.Run(`{}`, "hooks", "run", "--harness", "claude", "--config-dir", e.Config)
	e.Run(`{}`, "hooks", "run", "--harness", "Bad_Name", "--config-dir", e.Config)

	check := e.checker()
	formats := []string{"claude", "codex", "cursor", "windsurf", "opencode", "gemini", "copilot"}
	for _, format := range formats {
		e.installHook(format, format, filepath.Join(e.Home, "."+format, "hooks.json"), check, 5000)
	}
	e.installHook("custom-acme", "custom", filepath.Join(e.Home, "acme.json"), check, 5000)

	standard := `{"hook_event_name":"PreToolUse","session_id":"s1","tool_use_id":"t1","tool_name":"Bash","tool_input":{"command":"ls","n":1.50},"cwd":"/work","model":"m","extra":"dropped","turn_id":"turn"}`
	e.runHook("claude", standard)
	e.runHook("codex", "\ufeff"+standard)
	e.runHook("custom-acme", standard)
	e.runHook("cursor", `{"hook_event_name":"preToolUse","conversation_id":"c1","tool_use_id":"t2","tool_name":"Shell","tool_input":"{\"command\":\"ls\"}","cwd":"/work"}`)
	e.runHook("opencode", `{"session_id":"s3","tool_use_id":"t3","tool_name":"bash","tool_input":{"command":"pwd"}}`)
	e.runHook("gemini", `{"hook_event_name":"BeforeTool","session_id":"s4","tool_name":"run_shell_command","tool_input":{"command":"ls"}}`)
	e.runHook("copilot", `{"sessionId":"s5","toolCallId":"t5","toolName":"bash","toolArgs":"{\"command\":\"ls\"}","cwd":"/work","timestamp":1}`)
	e.runHook("copilot", `{"sessionId":"s5","toolName":"bash","toolArgs":{"command":"ls"}}`)
	e.runHook("windsurf", `{"agent_action_name":"pre_run_command","trajectory_id":"x","tool_info":{"command_line":"ls -la","cwd":"/work"}}`)
	e.runHook("windsurf", `{"agent_action_name":"pre_mcp_tool_use","session_id":"s6","tool_info":{"mcp_server_name":"durin","mcp_tool_name":"list","mcp_tool_arguments":"{\"a\":1}"}}`)
	e.runHook("windsurf", `{"agent_action_name":"pre_write_code","tool_info":{"file_path":"/work/a.txt","edits":[]}}`)
	e.runHook("windsurf", `{"agent_action_name":"post_run_command","tool_info":{}}`)

	// Invalid input never reaches the checker.
	e.runHook("claude", `{"hook_event_name":"PostToolUse","session_id":"s","tool_use_id":"t","tool_name":"x","tool_input":{}}`)
	e.runHook("claude", `not json`)
	e.runHook("claude", `[]`)
	e.runHook("cursor", `{"conversation_id":"c","tool_use_id":"t","tool_name":"x","tool_input":"not json"}`)
	e.runHook("claude", `{"pad":"`+strings.Repeat("x", 1_048_576)+`"}`)

	// The checker's decision.
	e.WriteFile(filepath.Join(e.Root, "checker-exit"), "2", 0o600)
	e.runHook("claude", standard)
	e.runHook("cursor", `{"conversation_id":"c","tool_use_id":"t","tool_name":"x","tool_input":{}}`)
	e.WriteFile(filepath.Join(e.Root, "checker-exit"), "1", 0o600)
	e.runHook("claude", standard)
	e.WriteFile(filepath.Join(e.Root, "checker-exit"), "sleep", 0o600)
	e.installHook("claude", "claude", filepath.Join(e.Home, ".claude", "hooks.json"), check, 100)
	e.runHook("claude", standard)
	_ = os.Remove(filepath.Join(e.Root, "checker-exit"))

	// A checker that disappeared, or is not executable.
	e.installHook("codex", "codex", filepath.Join(e.Home, ".codex", "hooks.json"), filepath.Join(e.Root, "missing-checker"), 5000)
	e.runHook("codex", standard)
	e.WriteFile(filepath.Join(e.Root, "plain"), "#!/bin/sh\nexit 0\n", 0o600)
	e.installHook("gemini", "gemini", filepath.Join(e.Home, ".gemini", "hooks.json"), filepath.Join(e.Root, "plain"), 5000)
	e.runHook("gemini", standard)

	// Status: an edited target reads as changed; a missing checker as unavailable.
	e.WriteFile(filepath.Join(e.Home, ".cursor", "hooks.json"), `{"edited":true}`, 0o600)
	e.Run("", "hooks", "status", "--config-dir", e.Config)

	// A damaged installation record fails status as a whole.
	e.WriteFile(filepath.Join(e.Config, "hooks", "zzz.json"), `{"format":"nope"}`, 0o600)
	e.Run("", "hooks", "status", "--config-dir", e.Config)
	e.runHook("zzz", standard)
}
