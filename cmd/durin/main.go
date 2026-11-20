// Command durin onboards an organization and connects it to the user's agent (src/cli.ts).
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/accessdurin/cli/internal/app"
	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/telemetry"
)

type command func(ctx context.Context, options *app.CliOptions, io *app.IO) error

func plain(f func(*app.CliOptions, *app.IO) error) command {
	return func(_ context.Context, options *app.CliOptions, io *app.IO) error { return f(options, io) }
}

var commands = map[string]command{
	"":         app.Wizard,
	"login":    app.Wizard,
	"signup":   app.Wizard,
	"install":  app.Wizard,
	"tools":    app.ToolCommand,
	"mcp":      app.McpCommand,
	"verify":   app.VerifyProfile,
	"profiles": plain(app.ProfileCommand),
	"logout":   app.LogoutCommand,
	"hooks":    hooks,
	"observe":  app.ObserveCommand,
	"observer": app.ObserverCommand,
}

func hooks(ctx context.Context, options *app.CliOptions, io *app.IO) error {
	action := ""
	if len(options.Positionals) >= 2 {
		action = options.Positionals[1]
	}
	switch action {
	case "report":
		return app.ReportCommand(options, io)
	case "flush":
		return app.FlushCommand(ctx, options, io)
	case "run":
	default:
		return app.HookCommand(options, io)
	}
	harness, _ := options.Values.String("harness")
	if event, ok := options.Values.String("event"); ok {
		return app.RunHookEvent(options, io, harness, event)
	}
	stdin, forwarded := app.ForwardHookInput(options.Directory, harness, io.Stdin)
	defer forwarded()
	installation, err := app.ReadHookInstallation(options.Directory, harness)
	if err != nil {
		return err
	}
	if err := app.RunHook(installation, stdin); err != nil {
		return err
	}
	if installation.Format == "cursor" {
		_, err = io.Stdout.Write([]byte(`{"permission":"allow"}` + "\n"))
	}
	return err
}

func durin(ctx context.Context, args []string, io *app.IO) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	options, err := app.ParseCliOptions(args)
	if err != nil {
		return err
	}
	if options.Values.Flag("help") {
		_, err := io.Stdout.Write([]byte(app.Help))
		return err
	}
	if err := app.ValidateCommandArguments(options); err != nil {
		return err
	}
	name := ""
	if len(options.Positionals) > 0 {
		name = options.Positionals[0]
	}
	handler, ok := commands[name]
	if !ok {
		return clierror.New("Unknown command. Run durin --help.")
	}
	if err := app.AssertPrivateDirectory(options.Directory); err != nil {
		return err
	}
	return handler(ctx, options, io)
}

const genericFailure = "Durin could not complete this request. Run durin in a terminal to verify your profile and authorization; retain the same idempotency key when retrying a tool call."

// errorMessage is what the user sees: an expected error's message, or a generic one.
func errorMessage(err error, localHook bool) string {
	if e, ok := clierror.As(err); ok {
		return e.Message
	}
	if localHook {
		return "Durin could not complete this local hook check."
	}
	if a, ok := err.(*app.ArgumentError); ok && a.Code == "ERR_PARSE_ARGS_UNKNOWN_OPTION" {
		return "Unknown option. Run durin --help."
	}
	return genericFailure
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, isTTY func() bool) int {
	localHook := len(args) >= 2 && args[0] == "hooks" && args[1] == "run"
	// Reporting runs beside the agent: it stays silent and never fails a tool call.
	reportingHook := len(args) >= 2 && args[0] == "hooks" && (args[1] == "report" || args[1] == "flush")
	// Hooks and the observer handle captured content; they never initialize telemetry.
	observe := len(args) >= 1 && args[0] == "observe"
	if !localHook && !observe && !reportingHook {
		telemetry.Initialize()
	}
	streams := &app.IO{Stdin: stdin, Stdout: stdout, Stderr: stderr, IsTTY: isTTY}
	err := telemetry.TraceCommand(func(ctx context.Context) error { return durin(ctx, args, streams) })
	code := streams.ExitCode
	if reportingHook {
		return 0
	}
	if err != nil && app.IsCaptureHook(args) {
		// A capture-only hook never blocks or fails the harness.
		return 0
	}
	if err != nil {
		fmt.Fprint(stderr, app.TerminalText(errorMessage(err, localHook))+"\n")
		code = 1
		if localHook {
			code = 2
		} else if !observe {
			telemetry.ReportUnexpected(err)
		}
	}
	telemetry.Flush()
	return code
}

func main() {
	isTTY := func() bool { return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) }
	app.TrustExtraCACertificates(os.Stderr)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, isTTY))
}
