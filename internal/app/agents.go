package app

import (
	"os"
	"path/filepath"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

type agentDefinition struct {
	config contracts.AgentConfig
	path   func(home string, env Env) string
}

func lookup(env Env, key string) (string, bool) { return env.Lookup(key) }

// envOr is env[key] ?? fallback: an empty value is kept.
func envOr(env Env, key, fallback string) string {
	if v, ok := lookup(env, key); ok {
		return v
	}
	return fallback
}

// envTruthy is env[key] || fallback: an empty value falls back.
func envTruthy(env Env, key, fallback string) string {
	if v, ok := lookup(env, key); ok && v != "" {
		return v
	}
	return fallback
}

func openCodePath(home string, env Env) string {
	if v, ok := lookup(env, "OPENCODE_CONFIG"); ok && v != "" {
		return v
	}
	base := filepath.Join(envOr(env, "XDG_CONFIG_HOME", filepath.Join(home, ".config")), "opencode", "opencode.json")
	if _, err := os.Stat(base + "c"); err == nil {
		return base + "c"
	}
	return base
}

func obj(members ...jsjson.Member) *jsjson.Object { return jsjson.Obj(members...) }

// AgentKeys are the built-in harnesses, in the order setup offers them.
var AgentKeys = []string{"claude", "codex", "cursor", "windsurf", "gemini", "copilot", "opencode", "pi", "kiro"}

// piAgentDirectory is pi's agent directory: $PI_CODING_AGENT_DIR or ~/.pi/agent.
func piAgentDirectory(home string, env Env) string {
	return envTruthy(env, "PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
}

var agentDefinitions = map[string]agentDefinition{
	"claude": {contracts.AgentConfig{
		Name: "Claude Code", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(jsjson.KV("type", jsjson.String("http"))), Bridge: "arguments",
		BridgeTransport: obj(jsjson.KV("type", jsjson.String("stdio"))),
		Authentication:  "Open Claude Code and use /mcp to authenticate Durin.",
	}, func(home string, env Env) string {
		return filepath.Join(envTruthy(env, "CLAUDE_CONFIG_DIR", home), ".claude.json")
	}},
	"codex": {contracts.AgentConfig{
		Name: "Codex", Format: "toml", ServerKey: "mcp_servers", URLKey: "url",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(),
		Authentication: "Run `codex mcp login durin` to authenticate.",
	}, func(home string, env Env) string {
		return filepath.Join(envOr(env, "CODEX_HOME", filepath.Join(home, ".codex")), "config.toml")
	}},
	"cursor": {contracts.AgentConfig{
		Name: "Cursor", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(),
		Authentication: "Open Cursor MCP settings and connect Durin to authenticate.",
	}, func(home string, _ Env) string { return filepath.Join(home, ".cursor", "mcp.json") }},
	"windsurf": {contracts.AgentConfig{
		Name: "Windsurf", Format: "jsonc", ServerKey: "mcpServers", URLKey: "serverUrl",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(),
		Authentication: "Open Windsurf Cascade MCP settings and authenticate Durin.",
	}, func(home string, _ Env) string { return filepath.Join(home, ".codeium", "windsurf", "mcp_config.json") }},
	"gemini": {contracts.AgentConfig{
		Name: "Gemini CLI", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(), Authentication: "",
	}, func(home string, _ Env) string { return filepath.Join(home, ".gemini", "settings.json") }},
	"copilot": {contracts.AgentConfig{
		Name: "GitHub Copilot CLI", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(), Bridge: "arguments",
		BridgeTransport: obj(jsjson.KV("type", jsjson.String("local")), jsjson.KV("tools", jsjson.Strings("*"))),
		Authentication:  "",
	}, func(home string, env Env) string {
		return filepath.Join(envOr(env, "COPILOT_HOME", filepath.Join(home, ".copilot")), "mcp-config.json")
	}},
	"opencode": {contracts.AgentConfig{
		Name: "OpenCode", Format: "jsonc", ServerKey: "mcp", URLKey: "url",
		Transport:       obj(jsjson.KV("type", jsjson.String("remote")), jsjson.KV("enabled", jsjson.Bool(true))),
		Bridge:          "command-array",
		BridgeTransport: obj(jsjson.KV("type", jsjson.String("local")), jsjson.KV("enabled", jsjson.Bool(true))),
		Authentication:  "Run `opencode mcp auth durin` to authenticate.",
	}, openCodePath},
	"pi": {contracts.AgentConfig{
		Name: "pi", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(), Authentication: "",
	}, func(home string, env Env) string { return filepath.Join(piAgentDirectory(home, env), "mcp.json") }},
	"kiro": {contracts.AgentConfig{
		Name: "Kiro", Format: "jsonc", ServerKey: "mcpServers", URLKey: "url",
		Transport: obj(), Bridge: "arguments", BridgeTransport: obj(),
		Authentication: "Open Kiro's MCP server panel to authenticate Durin.",
	}, func(home string, _ Env) string { return filepath.Join(home, ".kiro", "settings", "mcp.json") }},
}

// AgentsFor is agentsFor(home, env): every built-in harness's configuration.
func AgentsFor(home string, env Env) map[string]contracts.AgentConfig {
	out := map[string]contracts.AgentConfig{}
	for _, key := range AgentKeys {
		def := agentDefinitions[key]
		agent := def.config
		agent.Transport = def.config.Transport.Clone()
		agent.BridgeTransport = def.config.BridgeTransport.Clone()
		agent.ConfigPath = def.path(home, env)
		root := harnessRoots(home, env)[key]
		agent.ID = key
		agent.SkillsDirectory = filepath.Join(root, "skills")
		switch key {
		case "codex":
			agent.SkillsDirectory = filepath.Join(home, ".agents", "skills")
		case "kiro":
			agent.SkillsDirectory = "" // Kiro has no documented user skills directory
		}
		agent.HookConfig = &contracts.HookConfig{Path: hookPath(key, root), Format: contracts.HookFormat(key)}
		out[key] = agent
	}
	return out
}

// LocalAgents is localAgents(): the harnesses for this user.
func LocalAgents() map[string]contracts.AgentConfig { return AgentsFor(Homedir(), ProcessEnv) }

// AgentOption is one entry of the agent menu.
type AgentOption struct{ Value, Label, Hint string }

// AgentOptions is agentOptions(): each built-in harness with whether it is installed, then the
// custom harness entry.
func AgentOptions() []AgentOption {
	agents := LocalAgents()
	var out []AgentOption
	for _, key := range AgentKeys {
		hint := "not detected; install before continuing"
		if AgentInstalled(key, ProcessEnv, currentPlatform()) {
			hint = "installed"
		}
		out = append(out, AgentOption{Value: key, Label: agents[key].Name, Hint: hint})
	}
	return append(out, AgentOption{Value: "custom", Label: "Custom harness", Hint: "Use a harness descriptor JSON file"})
}

func harnessRoots(home string, env Env) map[string]string {
	return map[string]string{
		"claude":   envOr(env, "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")),
		"codex":    envOr(env, "CODEX_HOME", filepath.Join(home, ".codex")),
		"cursor":   filepath.Join(home, ".cursor"),
		"windsurf": filepath.Join(home, ".codeium", "windsurf"),
		"gemini":   filepath.Join(home, ".gemini"),
		"copilot":  envOr(env, "COPILOT_HOME", filepath.Join(home, ".copilot")),
		"opencode": filepath.Join(envOr(env, "XDG_CONFIG_HOME", filepath.Join(home, ".config")), "opencode"),
		"pi":       piAgentDirectory(home, env),
		"kiro":     filepath.Join(home, ".kiro"),
	}
}

func hookPath(id, root string) string {
	special := map[string]string{
		"claude":   "settings.json",
		"gemini":   "settings.json",
		"copilot":  filepath.Join("hooks", "durin.json"),
		"opencode": filepath.Join("plugins", "durin.js"),
		"pi":       filepath.Join("extensions", "durin.ts"),
		"kiro":     filepath.Join("hooks", "durin.json"),
	}
	if name, ok := special[id]; ok {
		return filepath.Join(root, name)
	}
	return filepath.Join(root, "hooks.json")
}
