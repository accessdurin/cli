package app

import (
	"context"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp"
	"github.com/accessdurin/cli/internal/mcp/jsonv"
	"github.com/accessdurin/cli/internal/zod"
)

const bridgeVerificationTimeout = 15 * time.Second

// VerifySavedBridge starts the bridge exactly as the agent's saved configuration does and
// returns how many tools its authenticated catalog lists (src/verify-bridge.ts).
func VerifySavedBridge(ctx context.Context, agent contracts.AgentConfig, config BridgeConfiguration) (int, error) {
	raw, found, err := ReadConfig(agent.ConfigPath)
	if err != nil {
		return 0, err
	}
	if found {
		var updated string
		if updated, err = UpdatedBridgeConfig(raw, agent, config); err != nil {
			return 0, err
		}
		found = updated == raw
	}
	if !found {
		return 0, clierror.New("The saved agent configuration is missing its organization bridge. Run durin to resume.")
	}
	transport := mcp.NewStdioClientTransport(mcp.StdioServerParameters{
		Command: config.ExecutablePath,
		Args:    serveArguments(config),
		Stderr:  "inherit",
		Env:     bridgeEnvironment(ProcessEnv),
	})
	client := mcp.NewClient(mcp.Implementation{Name: "Durin setup verification", Version: "1.0.0"}, mcp.ClientOptions{})
	defer client.Close()
	options := mcp.RequestOptions{Timeout: bridgeVerificationTimeout}
	if err := client.Connect(ctx, transport, options); err != nil {
		return 0, err
	}
	result, err := client.ListTools(ctx, nil, options)
	if err != nil {
		return 0, err
	}
	text, err := jsonv.Stringify(result)
	if err != nil {
		return 0, err
	}
	catalog, err := jsjson.Parse(text)
	if err != nil {
		return 0, err
	}
	parsed, err := zod.Parse(contracts.McpToolsListSchema, catalog)
	if err != nil {
		return 0, err
	}
	tools, _ := parsed.(*jsjson.Object).GetArray("tools")
	return len(tools), nil
}

// bridgeInheritedVariables are passed to the bridge, besides the MCP SDK's default
// environment, when set: telemetry opt-outs and what the bridge needs to reach the OS
// credential store. On Linux the Secret Service is on the session bus, which is found only
// through DBUS_SESSION_BUS_ADDRESS or XDG_RUNTIME_DIR. The CA variables let the bridge trust
// what setup trusts: NODE_EXTRA_CA_CERTS as the TypeScript CLI passed it, and SSL_CERT_FILE
// and SSL_CERT_DIR, which Go reads instead.
var bridgeInheritedVariables = []string{
	"DO_NOT_TRACK", "NODE_ENV", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
}

// bridgeEnvironment is the SDK's default environment plus bridgeInheritedVariables. Nothing
// else from this process (tokens, unrelated secrets) reaches the bridge.
func bridgeEnvironment(env Env) map[string]string {
	out := mcp.GetDefaultEnvironment()
	for _, key := range bridgeInheritedVariables {
		if value, ok := env.Lookup(key); ok && (value != "" || !emptyMeansUnset(key)) {
			out[key] = value
		}
	}
	return out
}

// emptyMeansUnset: TypeScript passed NODE_EXTRA_CA_CERTS only when it was non-empty; the
// other variables are passed whenever they are defined.
func emptyMeansUnset(key string) bool {
	return key == "NODE_EXTRA_CA_CERTS" || key == "SSL_CERT_FILE" || key == "SSL_CERT_DIR"
}
