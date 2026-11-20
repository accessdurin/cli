package app

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/jsjson"
)

// McpCommand is `durin mcp serve|endpoint` (src/mcp-command.ts). serve relays the agent's MCP
// session on stdio until stdin ends or the first SIGINT or SIGTERM.
func McpCommand(ctx context.Context, options *CliOptions, streams *IO) error {
	connection, err := NewProfileConnection(ctx, options, streams.Stderr)
	if err != nil {
		return err
	}
	switch positional(options, 1) {
	case "endpoint":
		return PrintJSON(streams.Stdout, jsjson.Obj(
			jsjson.KV("profile", jsjson.String(connection.Profile.ID)),
			jsjson.KV("resource", jsjson.String(connection.Resource)),
			jsjson.KV("authentication", jsjson.String("Authorize this resource in your MCP client; its grant is separate from the CLI grant.")),
		))
	case "serve":
	default:
		return clierror.New("Use durin mcp serve or durin mcp endpoint.")
	}
	ledger, err := NewFileCallLedger(options.Directory, connection.Profile.ID, nil)
	if err != nil {
		return err
	}
	bridge, err := NewMcpStdioBridge(McpStdioBridgeOptions{
		Resource:      connection.Resource,
		Authorization: connection.Authorization,
		Fetch:         connection.Fetch,
		Input:         streams.Stdin,
		Output:        streams.Stdout,
		Development:   connection.Development,
		Calls:         NewBridgeCalls(ledger),
		Serialize:     connection.Serialize,
		OnError:       func(message string) { _, _ = io.WriteString(streams.Stderr, message+"\n") },
		Marker:        connection.Marker,
		Warn:          connection.Warn,
	})
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for {
			select {
			case received := <-signals:
				// process.once: a second signal of the same kind takes its default action.
				signal.Reset(received)
				_ = bridge.Close()
			case <-bridge.Done():
				return
			}
		}
	}()
	if err := bridge.Start(ctx); err != nil {
		return err
	}
	bridge.Wait()
	return nil
}

// VerifyProfile is `durin verify` (src/mcp-command.ts): the profile's authenticated catalog.
func VerifyProfile(ctx context.Context, options *CliOptions, streams *IO) error {
	catalog, err := listTools(ctx, options, streams)
	if err != nil {
		return err
	}
	tools, _ := catalog.GetArray("tools")
	return PrintJSON(streams.Stdout, jsjson.Obj(
		jsjson.KV("verified", jsjson.Bool(true)),
		jsjson.KV("availableTools", jsjson.Int(int64(len(tools)))),
		jsjson.KV("detail", jsjson.String("Authenticated gateway catalog verified. This does not prove the agent harness has been opened.")),
	))
}
