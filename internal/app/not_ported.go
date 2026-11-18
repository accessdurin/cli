package app

import (
	"github.com/accessdurin/cli/internal/clierror"
)

// TODO(go-port): the wizard stops where the setup step of the Go port takes over
// (src/commands/wizard.ts with setup.ts and terminal-ui.ts). It already performs the checks
// that come before that point in the TypeScript CLI, so its failures match it, and then
// returns errNotPorted.
var errNotPorted = clierror.New("This command is not yet available in the Go build of durin.")

// Wizard is `durin`, login, signup and install (src/commands/wizard.ts).
func Wizard(options *CliOptions, io *IO) error {
	if io.IsTTY == nil || !io.IsTTY() {
		return clierror.New("Run durin in an interactive terminal to complete onboarding.")
	}
	return errNotPorted // TODO(go-port): runSetup with the terminal UI.
}
