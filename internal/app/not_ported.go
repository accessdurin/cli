package app

import (
	"context"
	"net/http"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/ports"
)

// TODO(go-port): the commands below stop where the MCP/OAuth step of the Go port takes over
// (setup-mcp.ts, logout.ts, mcp-command.ts, tool-commands.ts, profile-commands.ts
// logoutCommand). Each already performs the checks that come before that point in the
// TypeScript CLI, so their failures match it, and then returns errNotPorted.
var errNotPorted = clierror.New("This command is not yet available in the Go build of durin.")

// McpCommand is `durin mcp serve|endpoint` (src/mcp-command.ts).
func McpCommand(options *CliOptions, io *IO) error {
	if _, err := ConnectionProfile(options); err != nil {
		return err
	}
	return errNotPorted // TODO(go-port): profileConnection, McpStdioBridge, endpoint output.
}

// VerifyProfile is `durin verify` (src/mcp-command.ts).
func VerifyProfile(options *CliOptions, io *IO) error {
	if _, err := ConnectionProfile(options); err != nil {
		return err
	}
	return errNotPorted // TODO(go-port): withProfileClient(listTools).
}

// ToolCommand is `durin tools list|inspect|call` (src/tool-commands.ts).
func ToolCommand(options *CliOptions, io *IO) error {
	switch positional(options, 1) {
	case "list", "inspect":
		if _, err := ConnectionProfile(options); err != nil {
			return err
		}
	case "call":
	default:
		return clierror.New("Use durin tools list, inspect <name>, or call <name>.")
	}
	return errNotPorted // TODO(go-port): tool listing, inspection and calls over the remote MCP client.
}

// LogoutCommand is `durin logout` (src/profile-commands.ts).
func LogoutCommand(options *CliOptions, io *IO) error {
	if _, err := LoadSelectedProfile(options); err != nil {
		return err
	}
	return errNotPorted // TODO(go-port): retryPendingRevocations and signOutMcp.
}

// mcpIssuer is mcpIssuer (src/setup-mcp.ts): the personal endpoint's one trusted
// authorization server, from its protected resource metadata. Setup stops here until the
// MCP step is ported, before it saves a profile or touches the agent's configuration.
func mcpIssuer(ctx context.Context, resource string, request *http.Client, scope contracts.CliHostScope) (string, error) {
	return "", errNotPorted // TODO(go-port): discoverOAuthProtectedResourceMetadata and host checks.
}

// authorizeMcp is authorizeMcp (src/setup-mcp.ts), run under the profile lock.
func authorizeMcp(ctx context.Context, options authorizeMcpOptions) (int, error) {
	return 0, errNotPorted // TODO(go-port): McpOAuthProvider, the loopback callback and RemoteMcpClient.
}

// RetryPendingRevocations is retryPendingRevocations (src/logout.ts).
func RetryPendingRevocations(ctx context.Context, directory string, profile contracts.CliProfile, store ports.CredentialStore, fetch *http.Client) error {
	return errNotPorted // TODO(go-port): revoke the grants FilePendingRevocations holds.
}
