package app

import (
	"context"
	"io"
	"time"
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
	profile, err := LoadSelectedProfile(options)
	if err != nil {
		return nil
	}
	scoped := withProfileOption(options, profile.ID)
	connect := func(work func(client ToolCaller) error) error {
		_, err := WithProfileClient(ctx, scoped, io.Discard, func(client *RemoteMcpClient) (struct{}, error) {
			return struct{}{}, work(client)
		})
		return err
	}
	_, _ = FlushSpool(ctx, SpoolDirectory(options.Directory, profile.ID), connect, time.Now)
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
