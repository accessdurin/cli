package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/jsjson"
)

var testEndpoint = "https://mcp.getdurin.com/mcp/u/" + strings.Repeat("a", 64)

func TestAgentConfigurationAdapters(t *testing.T) {
	cases := []struct{ key, path, root, value string }{
		{"claude", ".claude.json", "mcpServers", `{"type":"http","url":"` + testEndpoint + `"}`},
		{"codex", ".codex/config.toml", "mcp_servers", `{"url":"` + testEndpoint + `"}`},
		{"cursor", ".cursor/mcp.json", "mcpServers", `{"url":"` + testEndpoint + `"}`},
		{"windsurf", ".codeium/windsurf/mcp_config.json", "mcpServers", `{"serverUrl":"` + testEndpoint + `"}`},
		{"opencode", ".config/opencode/opencode.json", "mcp", `{"type":"remote","enabled":true,"url":"` + testEndpoint + `"}`},
	}
	for _, c := range cases {
		home := tempDir(t)
		agent := AgentsFor(home, EnvMap{})[c.key]
		if agent.ConfigPath != filepath.Join(home, c.path) {
			t.Fatalf("%s path %s", c.key, agent.ConfigPath)
		}
		if err := WriteConfig(agent, testEndpoint); err != nil {
			t.Fatal(err)
		}
		initial := "{\n// keep comment\n\"theme\":\"keep\", \"" + c.root + "\": {\"other\": {\"command\":\"keep\"}}\n}"
		if agent.Format == "toml" {
			initial = "# keep comment\nmodel = \"keep\"\n[mcp_servers.other]\ncommand = \"keep\"\n"
		}
		writeFile(t, agent.ConfigPath, initial)
		_ = os.Chmod(agent.ConfigPath, 0o644)
		if err := WriteConfig(agent, testEndpoint); err != nil {
			t.Fatal(err)
		}
		saved := readFile(t, agent.ConfigPath)
		config, _ := ParseConfig(saved, agent.Format)
		servers, _ := config.GetObject(c.root)
		durin, _ := servers.Get("durin")
		other, _ := servers.Get("other")
		if !strings.Contains(saved, "keep comment") || jsjson.Stringify(durin) != c.value || jsjson.Stringify(other) != `{"command":"keep"}` {
			t.Fatalf("%s:\n%s", c.key, saved)
		}
		if endpoint, err := EndpointFrom(saved, agent); err != nil || endpoint != testEndpoint {
			t.Fatalf("%s endpoint %q %v", c.key, endpoint, err)
		}
		if info, _ := os.Stat(agent.ConfigPath); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", c.key, info.Mode())
		}
		if err := WriteConfig(agent, testEndpoint); err != nil || readFile(t, agent.ConfigPath) != saved {
			t.Fatalf("%s not idempotent: %v", c.key, err)
		}
	}
}

func TestAgentConfigurationRefusals(t *testing.T) {
	home := tempDir(t)
	claude := AgentsFor(home, EnvMap{})["claude"]
	for _, raw := range []string{"null", "[]", "{", `{"mcpServers":[]}`, `{"mcpServers":null}`} {
		writeFile(t, claude.ConfigPath, raw)
		if err := WriteConfig(claude, testEndpoint); err == nil || readFile(t, claude.ConfigPath) != raw {
			t.Fatalf("malformed %s: %v", raw, err)
		}
	}
	cursor := AgentsFor(home, EnvMap{})["cursor"]
	raw := `{"mcpServers":{"durin":{"url":"` + testEndpoint + `","headers":{"Authorization":"test-only"}}}}`
	if got, err := UpdatedConfig(raw, cursor, testEndpoint); err != nil || got != raw {
		t.Fatalf("existing credential not preserved: %v", err)
	}
	_, err := UpdatedConfig(raw, cursor, strings.Replace(testEndpoint, "/u/a", "/u/b", 1))
	wantCliError(t, err, "different Durin configuration")
	codex := AgentsFor(home, EnvMap{})["codex"]
	for _, raw := range []string{"a=[1 #", `mcp_servers = { other = { command = "keep" } }`} {
		if _, err := UpdatedConfig(raw, codex, testEndpoint); err == nil {
			t.Errorf("accepted TOML %q", raw)
		}
	}
	for _, server := range []string{
		`{"type":"stdio","url":"` + testEndpoint + `"}`, `{"type":"http","url":"` + testEndpoint + `","disabled":true}`,
		`{"url":"` + testEndpoint + `"}`, `{"type":"http","url":"` + testEndpoint + `","command":"node"}`, `{"type":"http","url":123}`,
	} {
		if _, err := EndpointFrom(`{"mcpServers":{"durin":`+server+`}}`, claude); err == nil {
			t.Errorf("accepted %s", server)
		}
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(home, "managed.json")
		writeFile(t, target, "{}")
		_ = os.Remove(claude.ConfigPath)
		_ = os.Symlink(target, claude.ConfigPath)
		if err := WriteConfig(claude, testEndpoint); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Lstat(claude.ConfigPath); info.Mode()&os.ModeSymlink == 0 || !strings.Contains(readFile(t, target), testEndpoint) {
			t.Fatal("symlinked configuration not updated through its link")
		}
	}
}

func TestAgentLocationsAndEndpoints(t *testing.T) {
	agents := AgentsFor("/unused", EnvMap{"CLAUDE_CONFIG_DIR": "/custom/claude", "CODEX_HOME": "/custom/codex", "XDG_CONFIG_HOME": "/custom/xdg", "OPENCODE_CONFIG": "/custom/open.jsonc"})
	for key, want := range map[string]string{"claude": "/custom/claude/.claude.json", "codex": "/custom/codex/config.toml", "opencode": "/custom/open.jsonc"} {
		if got := agents[key].ConfigPath; got != filepath.FromSlash(want) {
			t.Errorf("%s: %s", key, got)
		}
	}
	if got := AgentsFor("/unused", EnvMap{"XDG_CONFIG_HOME": "/custom/xdg"})["opencode"].ConfigPath; got != filepath.FromSlash("/custom/xdg/opencode/opencode.json") {
		t.Errorf("opencode: %s", got)
	}
	if got := AgentsFor("/home", EnvMap{"CLAUDE_CONFIG_DIR": ""})["claude"].ConfigPath; got != filepath.FromSlash("/home/.claude.json") {
		t.Errorf("an empty CLAUDE_CONFIG_DIR falls back: %s", got)
	}
	for _, invalid := range []string{"", "https://", "https://example.com", "http://mcp.getdurin.com/mcp/u/" + strings.Repeat("a", 64),
		testEndpoint + "?token=secret", testEndpoint + "#fragment", strings.Replace(testEndpoint, "https://", "https://name:secret@", 1),
		testEndpoint + "\n", strings.Replace(testEndpoint, "/mcp/u/", "/sse/", 1)} {
		if ValidateEndpoint(invalid) == "" {
			t.Errorf("accepted %q", invalid)
		}
	}
	if ValidateEndpoint(testEndpoint) != "" {
		t.Error("refused the issued endpoint format")
	}
}
