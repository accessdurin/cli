package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

// InstallHooks configures the harness to run checker before each tool call and records the
// installation in <config>/hooks/<harness>.json, which is written only after the harness
// configuration was saved.
func InstallHooks(agent contracts.AgentConfig, bridge BridgeConfiguration, checker contracts.HookChecker) error {
	if agent.ID == "" || agent.HookConfig == nil {
		return clierror.New("This harness has no configured hook adapter.")
	}
	if _, err := ValidateChecker(CheckerJSON(checker)); err != nil {
		return err
	}
	format := string(agent.HookConfig.Format)
	target := agent.HookConfig.Path
	installedAt := isoString(time.Now())
	path, err := HookInstallationPath(bridge.ConfigDirectory, agent.ID)
	if err != nil {
		return err
	}
	previous, hasPrevious, err := regularText(path)
	if err != nil {
		return err
	}
	var previousRecord *HookInstallation
	if hasPrevious {
		value, err := jsjson.ParseString(previous)
		if err != nil {
			return err
		}
		record, err := ParseHookInstallation(value)
		if err != nil {
			return err
		}
		previousRecord = &record
	}
	before, hasBefore, err := regularText(target)
	if err != nil {
		return err
	}
	raw := "{}"
	if hasBefore {
		raw = before
	}
	var after string
	if format == "opencode" {
		after = openCodePlugin(bridge, agent.ID, checker.TimeoutMs)
	} else {
		input := HookConfiguration{Format: format, Harness: agent.ID, Bridge: bridge, TimeoutMs: checker.TimeoutMs}
		if previousRecord != nil {
			input.PreviousHandler = previousRecord.Handler
		}
		if after, err = UpdatedHookConfig(raw, input); err != nil {
			return err
		}
	}
	change := FileChange{Path: target, After: after}
	if hasBefore {
		change.Before = &before
	}
	if format == "opencode" {
		if change, err = PlanManagedFile(target, after, pluginDigest(previous, hasPrevious)); err != nil {
			return err
		}
	}
	handler := GeneratedHookHandler(HookConfiguration{Format: format, Bridge: bridge, Harness: agent.ID, TimeoutMs: checker.TimeoutMs})
	saved := jsjson.StringifyIndent(jsjson.Obj(
		jsjson.KV("format", jsjson.String(format)),
		jsjson.KV("harness", jsjson.String(agent.ID)),
		jsjson.KV("checker", CheckerJSON(checker)),
		jsjson.KV("target", jsjson.String(target)),
		jsjson.KV("installedAt", jsjson.String(installedAt)),
		jsjson.KV("handler", handler),
		jsjson.KV("assetDigest", jsjson.String(textDigest(after))),
	), 2) + "\n"
	if err := saveFileChange(change); err != nil {
		return err
	}
	manifest := FileChange{Path: path, After: saved}
	if hasPrevious {
		manifest.Before = &previous
	}
	return saveFileChange(manifest)
}

// pluginDigest is the assetDigest of the previous installation record, read without
// validation as the TypeScript CLI did.
func pluginDigest(previous string, found bool) string {
	if !found || previous == "" {
		return ""
	}
	value, err := jsjson.ParseString(previous)
	if err != nil {
		return ""
	}
	if o, ok := value.(*jsjson.Object); ok {
		digest, _ := o.GetString("assetDigest")
		return digest
	}
	return ""
}

func openCodePlugin(bridge BridgeConfiguration, harness string, timeoutMs int) string {
	args := HookArguments(bridge, harness)
	return fmt.Sprintf(`// Managed by Durin CLI. Compatible with OpenCode V2 and V1 >= 1.18.29.
import { execFile } from 'node:child_process';
const check = (event) => new Promise((resolve, reject) => {
  const child = execFile(%s, %s, { timeout: %d, maxBuffer: 65536, windowsHide: true }, (error) => error ? reject(new Error('Durin local check denied or failed.')) : resolve());
  child.stdin.on('error', () => {});
  child.stdin.end(JSON.stringify(event));
});
export default {
  id: 'io.durin.local-checks',
  async setup(ctx) {
    await ctx.tool.hook('execute.before', (event) => check({ hook_event_name: 'PreToolUse', session_id: event.sessionID, tool_use_id: event.id, tool_name: event.tool, tool_input: event.input }));
  },
  async server({ directory }) {
    return { 'tool.execute.before': (input, output) => check({ hook_event_name: 'PreToolUse', session_id: input.sessionID, tool_use_id: input.callID, tool_name: input.tool, tool_input: output.args, cwd: directory }) };
  }
};
`, jsjson.Quote(args[0]), jsjson.Stringify(jsjson.Strings(args[1:]...)), timeoutMs+3000)
}

var hookRecordName = regexp.MustCompile(`^[a-z][a-z0-9-]*\.json$`)

// HookCommand is `durin hooks status`.
func HookCommand(options *CliOptions, io *IO) error {
	if positional(options, 1) != "status" {
		return clierror.New("Use durin hooks status, or run durin to configure hooks.")
	}
	directory := filepath.Join(options.Directory, "hooks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	results := jsjson.Array{}
	for _, entry := range entries {
		if !hookRecordName.MatchString(entry.Name()) {
			continue
		}
		status, err := hookStatus(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		results = append(results, status)
	}
	_, err = io.Stdout.Write([]byte(jsjson.StringifyIndent(results, 2) + "\n"))
	return err
}

func hookStatus(path string) (*jsjson.Object, error) {
	raw, found, err := regularText(path)
	if err != nil {
		return nil, err
	}
	if !found {
		raw = "{}"
	}
	value, err := jsjson.ParseString(raw)
	if err != nil {
		return nil, err
	}
	entry, err := ParseHookInstallation(value)
	if err != nil {
		return nil, err
	}
	config, hasConfig, err := regularText(entry.Target)
	if err != nil {
		return nil, err
	}
	checker := "available"
	if _, err := ValidateChecker(CheckerJSON(entry.Checker)); err != nil {
		checker = "unavailable"
	}
	configuration := "missing-or-changed"
	if hasConfig && textDigest(config) == entry.AssetDigest {
		configuration = "installed"
	}
	return jsjson.Obj(
		jsjson.KV("harness", jsjson.String(entry.Harness)),
		jsjson.KV("target", jsjson.String(entry.Target)),
		jsjson.KV("checker", jsjson.String(checker)),
		jsjson.KV("configuration", jsjson.String(configuration)),
		jsjson.KV("execution", jsjson.String("not-verified")),
	), nil
}
