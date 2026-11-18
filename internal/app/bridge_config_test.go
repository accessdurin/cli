package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/toml"
)

var testBridge = BridgeConfiguration{ExecutablePath: "/opt/durin/runtime/durin", ConfigDirectory: "/tmp/durin-profile", ProfileID: testProfileID}

const serveArgs = `"mcp","serve","--profile","` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `","--config-dir","/tmp/durin-profile"`

func bridgeEntryFor(t *testing.T, raw string, key string) string {
	t.Helper()
	agent := AgentsFor("/unused", EnvMap{})[key]
	config, err := ParseConfig(raw, agent.Format)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := config.GetObject(agent.ServerKey)
	return jsjson.Stringify(servers)
}

func TestBridgeConfigForEveryAgent(t *testing.T) {
	name := `"durin-aaaaaaaaaaaa":`
	cases := map[string]string{
		"claude":   `{` + name + `{"type":"stdio","command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"codex":    `{` + name + `{"command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"gemini":   `{` + name + `{"command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"copilot":  `{` + name + `{"type":"local","tools":["*"],"command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"cursor":   `{` + name + `{"command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"windsurf": `{` + name + `{"command":"/opt/durin/runtime/durin","args":[` + serveArgs + `]}}`,
		"opencode": `{` + name + `{"type":"local","enabled":true,"command":["/opt/durin/runtime/durin",` + serveArgs + `]}}`,
	}
	for key, want := range cases {
		home := tempDir(t)
		agent := AgentsFor(home, EnvMap{})[key]
		initial := "{ // keep comment\n \"theme\": \"keep\" }"
		if agent.Format == "toml" {
			initial = "# keep comment\nmodel=\"keep\"\n"
		}
		writeFile(t, agent.ConfigPath, initial)
		if err := WriteBridgeConfig(agent, testBridge); err != nil {
			t.Fatal(err)
		}
		raw := readFile(t, agent.ConfigPath)
		if !strings.Contains(raw, "keep comment") || !strings.Contains(raw, "keep") || bridgeEntryFor(t, raw, key) != want {
			t.Fatalf("%s:\n%s", key, raw)
		}
		if err := WriteBridgeConfig(agent, testBridge); err != nil || readFile(t, agent.ConfigPath) != raw {
			t.Fatalf("%s not idempotent: %v", key, err)
		}
	}
}

// Entries the TypeScript CLI wrote (node + <config>/runtime/cli.mjs) are replaced; anything
// else under the bridge's name is a conflict.
func TestBridgeConfigReplacesTheTypeScriptEntry(t *testing.T) {
	legacyArgs := `"/tmp/durin-profile/runtime/cli.mjs",` + serveArgs
	if filepath.Separator != '/' {
		t.Skip("POSIX paths")
	}
	jsonc := map[string]string{
		"claude":   "{\n  // keep comment\n  \"mcpServers\": {\n    \"durin-aaaaaaaaaaaa\": {\"type\":\"stdio\",\"command\":\"/usr/bin/node\",\"args\":[" + legacyArgs + "]},\n    \"other\": {\"command\":\"x\"} // other server\n  }\n}\n",
		"opencode": "{\n  // keep comment\n  \"mcp\": {\"durin-aaaaaaaaaaaa\": {\"type\":\"local\",\"enabled\":true,\"command\":[\"/usr/bin/node\"," + legacyArgs + "]}}\n}\n",
	}
	for key, raw := range jsonc {
		agent := AgentsFor("/unused", EnvMap{})[key]
		updated, err := UpdatedBridgeConfig(raw, agent, testBridge)
		if err != nil || strings.Contains(updated, "cli.mjs") || !strings.Contains(updated, "keep comment") || !strings.Contains(updated, "/opt/durin/runtime/durin") {
			t.Fatalf("%s: %v\n%s", key, err, updated)
		}
		if key == "claude" && !strings.Contains(updated, "// other server") {
			t.Fatalf("comment lost:\n%s", updated)
		}
	}

	codex := AgentsFor("/unused", EnvMap{})["codex"]
	written, _ := toml.Stringify(jsjson.Obj(jsjson.KV("mcp_servers", jsjson.Obj(jsjson.KV("durin-aaaaaaaaaaaa", jsjson.Obj(
		jsjson.KV("command", jsjson.String("/usr/bin/node")),
		jsjson.KV("args", jsjson.Strings(append([]string{"/tmp/durin-profile/runtime/cli.mjs"}, serveArguments(testBridge)...)...)),
	))))))
	raw := "# keep comment\nmodel = \"keep\"\n\n" + written + "\n# trailing note\n[mcp_servers.other]\ncommand = \"keep\"\n"
	updated, err := UpdatedBridgeConfig(raw, codex, testBridge)
	if err != nil || strings.Contains(updated, "cli.mjs") || !strings.Contains(updated, "# trailing note\n[mcp_servers.other]") || !strings.HasPrefix(updated, "# keep comment\nmodel = \"keep\"\n\n[mcp_servers.durin-aaaaaaaaaaaa]\ncommand = \"/opt/durin/runtime/durin\"\n") {
		t.Fatalf("codex: %v\n%s", err, updated)
	}
	if again, err := UpdatedBridgeConfig(updated, codex, testBridge); err != nil || again != updated {
		t.Fatalf("codex not idempotent: %v", err)
	}

	claude := AgentsFor("/unused", EnvMap{})["claude"]
	for _, conflicting := range []string{
		`{"mcpServers":{"durin-aaaaaaaaaaaa":{"type":"stdio","command":"/usr/bin/node","args":["/elsewhere/runtime/cli.mjs",` + serveArgs + `]}}}`,
		`{"mcpServers":{"durin-aaaaaaaaaaaa":{"type":"stdio","command":"/usr/bin/node","args":[` + legacyArgs + `],"disabled":true}}}`,
		`{"mcpServers":{"durin-aaaaaaaaaaaa":{"type":"http","url":"https://example.com"}}}`,
	} {
		_, err := UpdatedBridgeConfig(conflicting, claude, testBridge)
		wantCliError(t, err, "conflicting agent configuration")
	}
	inline := `mcp_servers = { durin-aaaaaaaaaaaa = { command = "/usr/bin/node", args = [` + legacyArgs + `] } }` + "\n"
	_, err = UpdatedBridgeConfig(inline, codex, testBridge)
	wantCliError(t, err, "conflicting agent configuration")
}
