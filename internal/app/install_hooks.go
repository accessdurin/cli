package app

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

// managedHookFormats are harnesses whose hooks durin writes as its own file (a plugin,
// extension or hook file) rather than as entries in a shared configuration.
var managedHookFormats = map[string]bool{"opencode": true, "pi": true, "kiro": true}

// InstallHooks registers durin's handler for every hook event the harness supports and
// records the installation in <config>/hooks/<harness>.json, which is written only after the
// harness configuration was saved. A nil checker keeps the checker an earlier installation
// recorded; without one, the pre-tool event only captures, like every other event.
func InstallHooks(agent contracts.AgentConfig, bridge BridgeConfiguration, checker *contracts.HookChecker) error {
	if agent.ID == "" || agent.HookConfig == nil {
		return clierror.New("This harness has no configured hook adapter.")
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
	if checker == nil && previousRecord != nil {
		checker = previousRecord.Checker
	}
	if checker != nil {
		if _, err := ValidateChecker(CheckerJSON(*checker)); err != nil {
			return err
		}
	}
	if format == "codex" {
		if err := assertCodexHooksEnabled(agent.ConfigPath); err != nil {
			return err
		}
	}
	before, hasBefore, err := regularText(target)
	if err != nil {
		return err
	}
	raw := "{}"
	if hasBefore {
		raw = before
	}
	input := HookConfiguration{Format: format, Harness: agent.ID, Bridge: bridge, Checker: checker != nil}
	if checker != nil {
		input.TimeoutMs = checker.TimeoutMs
	}
	if previousRecord != nil {
		input.PreviousHandler = previousRecord.Handler
		input.PreviousHandlers = previousRecord.Handlers
	}
	var after string
	switch format {
	case "opencode":
		after = openCodePlugin(input)
	case "pi":
		after = piExtension(input)
	case "kiro":
		after = kiroHooks(input)
	default:
		if after, err = UpdatedHookConfig(raw, input); err != nil {
			return err
		}
	}
	change := FileChange{Path: target, After: after}
	if hasBefore {
		change.Before = &before
	}
	if managedHookFormats[format] {
		if change, err = PlanManagedFile(target, after, pluginDigest(previous, hasPrevious)); err != nil {
			return err
		}
	}
	members := []jsjson.Member{
		jsjson.KV("format", jsjson.String(format)),
		jsjson.KV("harness", jsjson.String(agent.ID)),
	}
	if checker != nil {
		members = append(members, jsjson.KV("checker", CheckerJSON(*checker)))
	}
	// An unchanged installation keeps its record (and its installedAt) as it is.
	if previousRecord != nil && change.Before != nil && *change.Before == after {
		installedAt = previousRecord.InstalledAt
	}
	members = append(members,
		jsjson.KV("target", jsjson.String(target)),
		jsjson.KV("installedAt", jsjson.String(installedAt)),
	)
	if format == "custom" {
		members = append(members, jsjson.KV("handler", GeneratedHookHandler(input)))
	} else if !managedHookFormats[format] {
		handlers := GeneratedHookHandlers(input)
		for _, event := range registeredEvents(format, input.Checker) {
			if event.Check && input.Checker {
				h, _ := handlers.GetObject(event.Name)
				members = append(members, jsjson.KV("handler", h))
				break
			}
		}
		members = append(members, jsjson.KV("handlers", handlers))
	}
	members = append(members, jsjson.KV("assetDigest", jsjson.String(textDigest(after))))
	saved := jsjson.StringifyIndent(jsjson.Obj(members...), 2) + "\n"
	if err := saveFileChange(change); err != nil {
		return err
	}
	manifest := FileChange{Path: path, After: saved}
	if hasPrevious {
		manifest.Before = &previous
	}
	return saveFileChange(manifest)
}

// assertCodexHooksEnabled refuses to configure hooks Codex was told not to run
// ([features] hooks = false).
func assertCodexHooksEnabled(configPath string) error {
	raw, found, err := regularText(configPath)
	if err != nil || !found {
		return err
	}
	config, err := ParseConfig(raw, "toml")
	if err != nil {
		return err
	}
	features, _ := config.GetObject("features")
	if features != nil && jsjson.Equal(get(features, "hooks"), jsjson.Bool(false)) {
		return clierror.New("Hooks are disabled in this harness. Review its settings before configuring local checks.")
	}
	return nil
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

func eventNames(format string, checker bool, check bool) []string {
	var out []string
	for _, e := range registeredEvents(format, checker) {
		if e.Check == check {
			out = append(out, e.Name)
		}
	}
	return out
}

// openCodePlugin is the OpenCode plugin: the pre-tool check (awaited and blocking only when
// a checker is configured), tool results, chat messages and every bus event except streamed
// message parts, each forwarded without waiting.
func openCodePlugin(input HookConfiguration) string {
	args := HookArguments(input.Bridge, input.Harness)
	return fmt.Sprintf(`// Managed by Durin CLI. Compatible with OpenCode V2 and V1 >= 1.18.29.
import { execFile } from 'node:child_process';
const durin = %s;
const base = %s;
const checked = %t;
const run = (name, event, timeout) => new Promise((resolve, reject) => {
  let body;
  try { body = JSON.stringify(event); } catch { body = '{}'; }
  const child = execFile(durin, [...base, '--event', name], { timeout, maxBuffer: 65536, windowsHide: true }, (error) => error ? reject(new Error('Durin local check denied or failed.')) : resolve());
  child.stdin.on('error', () => {});
  child.stdin.end(body);
});
const capture = (name, event) => { if (/^[A-Za-z][A-Za-z0-9_.:-]{0,63}$/.test(name)) run(name, event, %d).catch(() => {}); };
const check = (event) => checked ? run('PreToolUse', event, %d) : capture('PreToolUse', event);
const streamed = new Set(['message.part.updated']);
export default {
  id: 'io.durin.local-checks',
  async setup(ctx) {
    await ctx.tool.hook('execute.before', (event) => check({ hook_event_name: 'PreToolUse', session_id: event.sessionID, tool_use_id: event.id, tool_name: event.tool, tool_input: event.input }));
    await ctx.tool.hook('execute.after', (event) => { capture('tool.execute.after', { session_id: event.sessionID, tool_use_id: event.id, tool_name: event.tool, tool_input: event.input, output: event.output }); });
    (async () => {
      try { for await (const e of ctx.event.subscribe({ signal: new AbortController().signal })) if (e && !streamed.has(e.type)) capture(e.type, e); } catch {}
    })();
  },
  async server({ directory }) {
    return {
      'tool.execute.before': (input, output) => check({ hook_event_name: 'PreToolUse', session_id: input.sessionID, tool_use_id: input.callID, tool_name: input.tool, tool_input: output.args, cwd: directory }),
      'tool.execute.after': (input, output) => { capture('tool.execute.after', { session_id: input.sessionID, tool_use_id: input.callID, tool_name: input.tool, output, cwd: directory }); },
      'chat.message': (input, output) => { capture('chat.message', { input, output, cwd: directory }); },
      event: ({ event }) => { if (event && !streamed.has(event.type)) capture(event.type, event); },
    };
  }
};
`, jsjson.Quote(args[0]), jsjson.Stringify(jsjson.Strings(args[1:]...)), input.Checker, captureTimeoutSec*1000, input.TimeoutMs+3000)
}

// piExtension is the pi extension: tool_call runs the check (blocking the call when it
// denies or fails, only when a checker is configured); every other event is forwarded
// without waiting and changes nothing.
func piExtension(input HookConfiguration) string {
	args := HookArguments(input.Bridge, input.Harness)
	return fmt.Sprintf(`// Managed by Durin CLI.
import { spawn } from 'node:child_process';
const durin = %s;
const base = %s;
const checked = %t;
const events = %s;
function run(name: string, payload: unknown, timeoutMs: number): Promise<number> {
  return new Promise((resolve) => {
    let body: string;
    try { body = JSON.stringify(payload); } catch { body = '{}'; }
    let child: any;
    try { child = spawn(durin, [...base, '--event', name], { stdio: ['pipe', 'ignore', 'ignore'], windowsHide: true }); } catch { resolve(-1); return; }
    const timer = setTimeout(() => { child.kill(); resolve(-1); }, timeoutMs);
    child.on('error', () => { clearTimeout(timer); resolve(-1); });
    child.on('close', (code: number | null) => { clearTimeout(timer); resolve(code ?? -1); });
    child.stdin.on('error', () => {});
    child.stdin.end(body);
  });
}
function sessionId(ctx: any): string | null {
  try { return ctx?.sessionManager?.getSessionId?.() ?? null; } catch { return null; }
}
export default function (pi: any) {
  pi.on('tool_call', async (event: any, ctx: any) => {
    const payload = { hook_event_name: 'PreToolUse', session_id: sessionId(ctx) ?? 'pi', tool_use_id: event.toolCallId, tool_name: event.toolName, tool_input: event.input, cwd: process.cwd() };
    if (!checked) { void run('tool_call', payload, %d); return; }
    if ((await run('tool_call', payload, %d)) !== 0) return { block: true, reason: 'Durin local check denied or failed.' };
  });
  for (const name of events) {
    pi.on(name, (event: any, ctx: any) => { void run(name, { hook_event_name: name, session_id: sessionId(ctx), event }, %d); });
  }
}
`, jsjson.Quote(args[0]), jsjson.Stringify(jsjson.Strings(args[1:]...)), input.Checker,
		jsjson.Stringify(jsjson.Strings(eventNames("pi", input.Checker, false)...)),
		captureTimeoutSec*1000, input.TimeoutMs+3000, captureTimeoutSec*1000)
}

// kiroHooks is ~/.kiro/hooks/durin.json in Kiro's v1 hook format: one command hook per
// trigger. Kiro adds a command's stdout to the agent's context, so handlers print nothing.
func kiroHooks(input HookConfiguration) string {
	hooks := jsjson.Array{}
	for _, event := range registeredEvents("kiro", input.Checker) {
		handler := input.forEvent(event)
		timeout := int64(captureTimeoutSec)
		if handler.checking() {
			timeout = int64(math.Ceil(float64(input.TimeoutMs)/1000)) + 3
		}
		hooks = append(hooks, jsjson.Obj(
			jsjson.KV("name", jsjson.String("durin-"+event.Name)),
			jsjson.KV("description", jsjson.String("Managed by Durin CLI")),
			jsjson.KV("trigger", jsjson.String(event.Name)),
			jsjson.KV("action", jsjson.Obj(jsjson.KV("type", jsjson.String("command")), jsjson.KV("command", jsjson.String(commandLine(handler))))),
			jsjson.KV("timeout", jsjson.Int(timeout)),
		))
	}
	return jsjson.StringifyIndent(jsjson.Obj(jsjson.KV("version", jsjson.String("v1")), jsjson.KV("hooks", hooks)), 2) + "\n"
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
	if entry.Checker == nil {
		checker = "none"
	} else if _, err := ValidateChecker(CheckerJSON(*entry.Checker)); err != nil {
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
