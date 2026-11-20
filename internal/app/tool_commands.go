package app

import (
	"context"
	"io"
	"os"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

// maxToolInput is the largest tool input --input accepts (1 MiB).
const maxToolInput = 1_048_576

// inputArguments reads --input (a JSON file, or - for stdin) and parses it with JSON.parse.
func inputArguments(path string, given bool, stdin io.Reader) (jsjson.Value, error) {
	if !given || path == "" {
		return nil, clierror.New("Provide --input <JSON file|-> and --idempotency-key <key>.")
	}
	input := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		input = file
	}
	source, err := io.ReadAll(io.LimitReader(input, maxToolInput+1))
	if err != nil {
		return nil, err
	}
	if len(source) > maxToolInput {
		return nil, clierror.New("Tool input exceeds 1 MiB.")
	}
	return jsjson.Parse(source)
}

func listTools(ctx context.Context, options *CliOptions, io *IO) (contracts.CliToolCatalog, error) {
	return WithProfileClient(ctx, options, io.Stderr, func(client *RemoteMcpClient) (contracts.CliToolCatalog, error) {
		return client.ListTools(ctx)
	})
}

func inspectTool(ctx context.Context, options *CliOptions, io *IO) error {
	catalog, err := listTools(ctx, options, io)
	if err != nil {
		return err
	}
	tools, _ := catalog.GetArray("tools")
	if len(options.Positionals) > 2 {
		for _, tool := range tools {
			if name, _ := tool.(*jsjson.Object).GetString("name"); name == options.Positionals[2] {
				return PrintJSON(io.Stdout, tool)
			}
		}
	}
	return clierror.New("Tool is unavailable in this organization. Run durin tools list.")
}

func callTool(ctx context.Context, options *CliOptions, io *IO) error {
	path, given := options.Values.String("input")
	arguments, err := inputArguments(path, given, io.Stdin)
	if err != nil {
		return err
	}
	var name, key *string
	if len(options.Positionals) > 2 {
		name = &options.Positionals[2]
	}
	if value, ok := options.Values.String("idempotency-key"); ok {
		key = &value
	}
	approval, _ := options.Values.String("approval-id")
	input, ok := contracts.ParseCliToolCall(name, arguments, key, approval)
	if !ok {
		return clierror.New("Invalid call. Supply a tool name, JSON object, and --idempotency-key; use --approval-id when required.")
	}
	result, err := WithProfileClient(ctx, options, io.Stderr, func(client *RemoteMcpClient) (contracts.McpToolResult, error) {
		return client.CallTool(ctx, input)
	})
	if err != nil {
		return err
	}
	if err := PrintJSON(io.Stdout, result); err != nil {
		return err
	}
	if isError, _ := result.GetBool("isError"); isError {
		io.ExitCode = 1
	}
	return nil
}

// ToolCommand is `durin tools list|inspect <name>|call <name>` (src/tool-commands.ts).
func ToolCommand(ctx context.Context, options *CliOptions, io *IO) error {
	switch positional(options, 1) {
	case "list":
		catalog, err := listTools(ctx, options, io)
		if err != nil {
			return err
		}
		return PrintJSON(io.Stdout, catalog)
	case "inspect":
		return inspectTool(ctx, options, io)
	case "call":
		return callTool(ctx, options, io)
	}
	return clierror.New("Use durin tools list, inspect <name>, or call <name>.")
}
