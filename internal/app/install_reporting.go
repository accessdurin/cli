package app

import (
	"runtime"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

// reportPlan is how one harness runs the post-tool report handler (src/install-reporting.ts).
type reportPlan struct {
	events  []string
	grouped bool
	handler func(command string) *jsjson.Object
}

// asyncReportHandler: Claude Code and Codex run the report in the background. Cursor and
// Windsurf have no background option, so the report command itself returns before any
// network call.
func asyncReportHandler(command string) *jsjson.Object {
	handler := jsjson.Obj(
		jsjson.KV("type", jsjson.String("command")),
		jsjson.KV("command", jsjson.String(command)),
		jsjson.KV("async", jsjson.Bool(true)),
		jsjson.KV("timeout", jsjson.Int(30)),
	)
	return handler
}

var reportPlans = map[string]reportPlan{
	"claude": {events: []string{"PostToolUse", "PostToolUseFailure"}, grouped: true, handler: asyncReportHandler},
	"codex":  {events: []string{"PostToolUse"}, grouped: true, handler: asyncReportHandler},
	"cursor": {events: []string{"afterMCPExecution"}, handler: func(command string) *jsjson.Object {
		return jsjson.Obj(jsjson.KV("command", jsjson.String(command)), jsjson.KV("timeout", jsjson.Int(10)))
	}},
	"windsurf": {events: []string{"post_mcp_tool_use"}, handler: func(command string) *jsjson.Object {
		return jsjson.Obj(jsjson.KV("command", jsjson.String(command)), jsjson.KV("show_output", jsjson.Bool(false)))
	}},
}

// ReportArguments is the command a harness runs after each MCP tool call:
// <executable> hooks report --harness <harness> --config-dir <directory>.
func ReportArguments(bridge BridgeConfiguration, harness string) []string {
	return []string{bridge.ExecutablePath, "hooks", "report", "--harness", harness, "--config-dir", bridge.ConfigDirectory}
}

func hasReportCommand(entry *jsjson.Object, command string, grouped bool) bool {
	if !grouped {
		return jsjson.Equal(get(entry, "command"), jsjson.String(command))
	}
	hooks, err := parseRecords(get(entry, "hooks"))
	if err != nil {
		return false
	}
	for _, hook := range hooks {
		if jsjson.Equal(get(hook, "command"), jsjson.String(command)) {
			return true
		}
	}
	return false
}

func reportEventEntries(raw, event string) ([]*jsjson.Object, error) {
	config, err := ParseConfig(raw, "jsonc")
	if err != nil {
		return nil, err
	}
	hooks, err := parseRecord(orDefault(get(config, "hooks"), jsjson.NewObject()))
	if err != nil {
		return nil, err
	}
	return parseRecords(orDefault(get(hooks, event), jsjson.Array{}))
}

func reportEntry(plan reportPlan, command string) jsjson.Value {
	handler := plan.handler(command)
	if !plan.grouped {
		return handler
	}
	return jsjson.Obj(jsjson.KV("matcher", jsjson.String("mcp__.*")), jsjson.KV("hooks", jsjson.Arr(handler)))
}

// reportSlot is where to write: the existing handler for the current or previous command,
// else a new entry.
func reportSlot(entries []*jsjson.Object, plan reportPlan, current, previous string) int {
	for _, command := range []string{current, previous} {
		if command == "" {
			continue
		}
		for i, entry := range entries {
			if hasReportCommand(entry, command, plan.grouped) {
				return i
			}
		}
	}
	return len(entries)
}

func withReportEvent(raw, event string, plan reportPlan, current, previous string) (string, error) {
	entries, err := reportEventEntries(raw, event)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if hasReportCommand(entry, current, plan.grouped) {
			return raw, nil
		}
	}
	return jsoncEdit(raw, []any{"hooks", event, reportSlot(entries, plan, current, previous)}, reportEntry(plan, current))
}

// UpdatedReportConfig adds durin's post-tool report handler to a harness hook file, keeping
// every other hook. A handler for previous (an earlier executable path) is replaced rather
// than duplicated.
func UpdatedReportConfig(raw, format, command, previous string) (string, error) {
	config, err := ParseConfig(raw, "jsonc")
	if err != nil {
		return "", err
	}
	if err := assertHooksEnabled(config); err != nil {
		return "", err
	}
	plan, ok := reportPlans[format]
	if !ok {
		return "", clierror.New("This harness has no local tool-call reporting adapter.")
	}
	current := raw
	if format == "cursor" && get(config, "version") == nil {
		if current, err = jsoncEdit(raw, []any{"version"}, jsjson.Number(1)); err != nil {
			return "", err
		}
	}
	for _, event := range plan.events {
		if current, err = withReportEvent(current, event, plan, command, previous); err != nil {
			return "", err
		}
	}
	return current, nil
}

// ReportingSupported reports whether the harness has a reporting adapter.
func ReportingSupported(agent contracts.AgentConfig) bool {
	return agent.ID != "" && agent.HookConfig != nil && string(agent.HookConfig.Format) == agent.ID && isReportFormat(agent.ID)
}

// ReportingAvailable is false when the harness has no adapter or has hooks turned off, so
// setup does not offer reporting.
func ReportingAvailable(agent contracts.AgentConfig) bool {
	if !ReportingSupported(agent) {
		return false
	}
	if agent.ID == "codex" && assertCodexHooksEnabled(agent.ConfigPath) != nil {
		return false
	}
	raw, found, err := regularText(agent.HookConfig.Path)
	if err != nil {
		return false
	}
	if !found {
		raw = "{}"
	}
	_, err = UpdatedReportConfig(raw, agent.ID, "durin-availability-check", "")
	return err == nil
}

// InstallReporting configures the harness to report calls to local MCP servers to this
// profile's organization, and records it in <config>/reporting/<harness>.json once the
// harness configuration was saved. The hook stays quiet while Durin Endpoint covers the
// harness.
func InstallReporting(agent contracts.AgentConfig, bridge BridgeConfiguration) error {
	if !ReportingSupported(agent) {
		return clierror.New("This harness has no local tool-call reporting adapter.")
	}
	harness, target := agent.ID, agent.HookConfig.Path
	command := harnessCommandLine(ReportArguments(bridge, harness), harness, nil)
	before, hasBefore, err := regularText(target)
	if err != nil {
		return err
	}
	path, err := ReportingPath(bridge.ConfigDirectory, harness)
	if err != nil {
		return err
	}
	previous, _ := ReadReporting(bridge.ConfigDirectory, harness)
	previousCommand := ""
	if previous != nil {
		previousCommand = previous.Command
	}
	raw := "{}"
	if hasBefore {
		raw = before
	}
	after, err := UpdatedReportConfig(raw, harness, command, previousCommand)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" && harness == "claude" && after != raw {
		// Claude Code runs PowerShell commands only when told to, as for the pre-tool hook.
		if after, err = withPowerShell(after, command); err != nil {
			return err
		}
	}
	record := func(installedAt string) string {
		return jsjson.StringifyIndent(jsjson.Obj(
			jsjson.KV("harness", jsjson.String(harness)),
			jsjson.KV("format", jsjson.String(harness)),
			jsjson.KV("profileId", jsjson.String(bridge.ProfileID)),
			jsjson.KV("target", jsjson.String(target)),
			jsjson.KV("command", jsjson.String(command)),
			jsjson.KV("mcpConfig", jsjson.Obj(
				jsjson.KV("configPath", jsjson.String(agent.ConfigPath)),
				jsjson.KV("format", jsjson.String(agent.Format)),
				jsjson.KV("serverKey", jsjson.String(agent.ServerKey)),
			)),
			jsjson.KV("installedAt", jsjson.String(installedAt)),
		), 2) + "\n"
	}
	change := FileChange{Path: target, After: after}
	if hasBefore {
		change.Before = &before
	}
	if err := saveFileChange(change); err != nil {
		return err
	}
	// Record ownership only after the harness configuration was saved. An unchanged
	// installation keeps its record (and its installedAt) as it is.
	saved, hasSaved, err := regularText(path)
	if err != nil {
		return err
	}
	content := record(isoString(time.Now()))
	if previous != nil && record(previous.InstalledAt) == saved {
		content = saved
	}
	manifest := FileChange{Path: path, After: content}
	if hasSaved {
		manifest.Before = &saved
	}
	return saveFileChange(manifest)
}

// withPowerShell sets shell: "powershell" on Claude Code report handlers for command.
func withPowerShell(raw, command string) (string, error) {
	current := raw
	for _, event := range reportPlans["claude"].events {
		entries, err := reportEventEntries(current, event)
		if err != nil {
			return "", err
		}
		for i, entry := range entries {
			hooks, err := parseRecords(get(entry, "hooks"))
			if err != nil {
				continue
			}
			for j, hook := range hooks {
				if jsjson.Equal(get(hook, "command"), jsjson.String(command)) && get(hook, "shell") == nil {
					if current, err = jsoncEdit(current, []any{"hooks", event, i, "hooks", j, "shell"}, jsjson.String("powershell")); err != nil {
						return "", err
					}
				}
			}
		}
	}
	return current, nil
}
