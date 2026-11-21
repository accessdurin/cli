package app

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/accessdurin/cli/internal/ports"
)

// ReportCommand is durin hooks report: it runs from a harness post-tool hook, prints nothing
// and never fails the agent (src/report-commands.ts).
func ReportCommand(options *CliOptions, streams *IO) error {
	harness, _ := options.Values.String("harness")
	_, _ = ReportHook(options.Directory, harness, streams.Stdin, ReportDependencies{})
	return nil
}

// FlushCommand is durin hooks flush: it delivers spooled calls for the selected (or active)
// profile. The report hook starts it detached; it prints nothing.
func FlushCommand(ctx context.Context, options *CliOptions, _ *IO) error {
	return flushProfile(ctx, options, NewNativeCredentialStore())
}

// flushProfile delivers the profile's spool, or deletes it when the profile holds no MCP
// tokens, so calls observed while signed out are never sent after the next sign-in. A store
// that cannot be read keeps the spool.
func flushProfile(ctx context.Context, options *CliOptions, store ports.CredentialStore) error {
	profile, err := LoadSelectedProfile(options)
	if err != nil {
		return nil
	}
	spool := SpoolDirectory(options.Directory, profile.ID)
	if _, found, err := store.Read("mcp:" + profile.ID + ":tokens"); err == nil && !found {
		_ = os.RemoveAll(spool)
		return nil
	}
	scoped := withProfileOption(options, profile.ID)
	connect := func(work func(client ToolCaller) error) error {
		_, err := WithProfileClient(ctx, scoped, io.Discard, func(client *RemoteMcpClient) (struct{}, error) {
			return struct{}{}, work(client)
		})
		return err
	}
	_, _ = FlushSpool(ctx, spool, connect, time.Now)
	return nil
}

// withProfileOption is options with --profile set to id.
func withProfileOption(options *CliOptions, id string) *CliOptions {
	scoped := *options
	scoped.Values = CliValues{Keys: append([]string(nil), options.Values.Keys...), strings: map[string]string{}, flags: map[string]bool{}}
	for k, v := range options.Values.strings {
		scoped.Values.strings[k] = v
	}
	for k, v := range options.Values.flags {
		scoped.Values.flags[k] = v
	}
	scoped.Values.store("profile", &id)
	return &scoped
}
