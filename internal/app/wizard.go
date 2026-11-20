package app

import (
	"context"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/ports"
)

// Wizard is `durin`, login, signup and install (src/commands/wizard.ts): interactive setup,
// which needs a terminal on stdin and stdout.
func Wizard(ctx context.Context, options *CliOptions, io *IO) error {
	if io.IsTTY == nil || !io.IsTTY() {
		return clierror.New("Run durin in an interactive terminal to complete onboarding.")
	}
	program, err := CurrentExecutable()
	if err != nil {
		return err
	}
	ui := TerminalUI(io.Stdin, io.Stdout)
	newPrompter(io.Stdin, io.Stdout).intro("Welcome to Durin")
	return RunSetup(ctx, SetupOptions{
		AppOrigin:   options.AppOrigin,
		Development: options.Development,
		Directory:   options.Directory,
		ProgramPath: program,
		UI:          ui,
		Intent:      accountIntent(positional(options, 0)),
	})
}

// accountIntent is the intent the command names: login or signup, else "" (ask).
func accountIntent(command string) ports.AccountIntent {
	if command == string(ports.IntentLogin) || command == string(ports.IntentSignup) {
		return ports.AccountIntent(command)
	}
	return ""
}
