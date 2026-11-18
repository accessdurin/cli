package app

import (
	"github.com/accessdurin/cli/internal/clierror"
)

// TODO(go-port): the commands below stop where the MCP/OAuth/setup step of the Go port takes
// over (src/commands/wizard.ts with setup.ts and terminal-ui.ts, mcp-command.ts,
// tool-commands.ts, profile-commands.ts logoutCommand). Each already performs the checks
// that come before that point in the TypeScript CLI, so their failures match it, and then
// returns errNotPorted.
var errNotPorted = clierror.New("This command is not yet available in the Go build of durin.")

// Wizard is `durin`, login, signup and install (src/commands/wizard.ts).
func Wizard(options *CliOptions, io *IO) error {
	if io.IsTTY == nil || !io.IsTTY() {
		return clierror.New("Run durin in an interactive terminal to complete onboarding.")
	}
	return errNotPorted // TODO(go-port): runSetup with the terminal UI.
}

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
