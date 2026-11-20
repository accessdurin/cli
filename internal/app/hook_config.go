package app

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"runtime"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

var hookArraySchema = zod.Array(recordSchema)

func grouped(format string) bool {
	return format == "claude" || format == "codex" || format == "gemini"
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func powershellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// HookArguments is the command a harness runs before each tool call:
// <executable> hooks run --harness <harness> --config-dir <directory>.
func HookArguments(bridge BridgeConfiguration, harness string) []string {
	return []string{bridge.ExecutablePath, "hooks", "run", "--harness", harness, "--config-dir", bridge.ConfigDirectory}
}

// legacyHookArguments is the hook command the TypeScript CLI configured, for node.
func legacyHookArguments(node string, bridge BridgeConfiguration, harness string) []string {
	return append([]string{node, legacyRuntime(bridge.ConfigDirectory)}, HookArguments(bridge, harness)[1:]...)
}

// HookConfiguration is what a harness's hook entry is generated from. Windows nil means this
// platform. PreviousHandler is the handler durin recorded at its last installation, which is
// replaced in place.
type HookConfiguration struct {
	Format          string
	Bridge          BridgeConfiguration
	Harness         string
	TimeoutMs       int
	Windows         *bool
	PreviousHandler *jsjson.Object
}

func commandLine(input HookConfiguration) string {
	args := HookArguments(input.Bridge, input.Harness)
	windows := runtime.GOOS == "windows"
	if input.Windows != nil {
		windows = *input.Windows
	}
	quoted := make([]string, len(args))
	if !windows {
		for i, a := range args {
			quoted[i] = shellQuote(a)
		}
		return strings.Join(quoted, " ")
	}
	for i, a := range args {
		quoted[i] = powershellQuote(a)
	}
	script := "& " + strings.Join(quoted, " ") + "; exit $LASTEXITCODE"
	// These harnesses already launch PowerShell. Exit that shell explicitly so a denial stays
	// exit 2 rather than PowerShell -Command normalizing it to 1.
	if input.Format == "claude" || input.Format == "cursor" {
		return script
	}
	return "powershell.exe -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(utf16le(script))
}

func utf16le(s string) []byte {
	units := jsjson.ToUTF16(s)
	out := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2*i:], u)
	}
	return out
}

// UpdatedHookConfig adds or updates durin's pre-tool hook in a harness's JSONC hook
// configuration, keeping every other hook, setting and comment.
func UpdatedHookConfig(raw string, input HookConfiguration) (string, error) {
	config, err := ParseConfig(raw, "jsonc")
	if err != nil {
		return "", err
	}
	if err := assertHooksEnabled(config); err != nil {
		return "", err
	}
	switch input.Format {
	case "custom":
		return customConfig(raw, config, input)
	case "copilot":
		return copilotConfig(raw, config, input)
	}
	return updateEvents(raw, config, input)
}

// orDefault is value ?? fallback.
func orDefault(value jsjson.Value, fallback jsjson.Value) jsjson.Value {
	return nullish(value, func() jsjson.Value { return fallback })
}

func parseRecord(value jsjson.Value) (*jsjson.Object, error) {
	out, err := zod.Parse(recordSchema, value)
	if err != nil {
		return nil, err
	}
	return out.(*jsjson.Object), nil
}

func parseRecords(value jsjson.Value) ([]*jsjson.Object, error) {
	out, err := zod.Parse(hookArraySchema, value)
	if err != nil {
		return nil, err
	}
	var records []*jsjson.Object
	for _, v := range out.(jsjson.Array) {
		records = append(records, v.(*jsjson.Object))
	}
	return records, nil
}

func assertHooksEnabled(config *jsjson.Object) error {
	hooksConfig, err := parseRecord(orDefault(get(config, "hooksConfig"), jsjson.NewObject()))
	if err != nil {
		return err
	}
	if jsjson.Equal(get(config, "disableAllHooks"), jsjson.Bool(true)) || jsjson.Equal(get(hooksConfig, "enabled"), jsjson.Bool(false)) {
		return clierror.New("Hooks are disabled in this harness. Review its settings before configuring local checks.")
	}
	return nil
}

func updateEvents(raw string, config *jsjson.Object, input HookConfiguration) (string, error) {
	hooks, err := parseRecord(orDefault(get(config, "hooks"), jsjson.NewObject()))
	if err != nil {
		return "", err
	}
	current := raw
	if input.Format == "cursor" {
		if current, err = versioned(raw, config); err != nil {
			return "", err
		}
	}
	for _, event := range hookEvents(input.Format) {
		groups, err := parseRecords(orDefault(get(hooks, event), jsjson.Array{}))
		if err != nil {
			return "", err
		}
		if current, err = upsertHandler(current, event, groups, input); err != nil {
			return "", err
		}
	}
	return current, nil
}

func upsertHandler(raw, event string, groups []*jsjson.Object, input HookConfiguration) (string, error) {
	handler := GeneratedHookHandler(input)
	location, err := handlerLocation(groups, input, handler)
	if err != nil {
		return "", err
	}
	if location != nil {
		return jsoncEdit(raw, append([]any{"hooks", event}, location...), handler)
	}
	var entry jsjson.Value = handler
	if grouped(input.Format) {
		entry = jsjson.Obj(jsjson.KV("matcher", jsjson.String(".*")), jsjson.KV("hooks", jsjson.Arr(handler)))
	}
	// Appending to the syntax tree keeps comments inside every existing handler.
	return jsoncEdit(raw, []any{"hooks", event, len(groups)}, entry)
}

func handlerLocation(groups []*jsjson.Object, input HookConfiguration, handler *jsjson.Object) ([]any, error) {
	for i, group := range groups {
		if !grouped(input.Format) {
			if sameHandler(group, handler, input.PreviousHandler) {
				return []any{i}, nil
			}
			continue
		}
		handlers, err := parseRecords(get(group, "hooks"))
		if err != nil {
			return nil, err
		}
		for j, h := range handlers {
			if sameHandler(h, handler, input.PreviousHandler) {
				return []any{i, "hooks", j}, nil
			}
		}
	}
	return nil, nil
}

func sameHandler(value, expected, previous *jsjson.Object) bool {
	if previous == nil {
		previous = jsjson.NewObject()
	}
	return sameInvocation(value, expected) || sameInvocation(value, previous)
}

// strictEquals is === on JSON values: objects and arrays are never equal across parses.
func strictEquals(a, b jsjson.Value) bool {
	switch a.(type) {
	case *jsjson.Object, jsjson.Array:
		return false
	}
	return (a == nil && b == nil) || (a != nil && jsjson.Equal(a, b))
}

func sameInvocation(value, expected *jsjson.Object) bool {
	if command, ok := get(expected, "command").(jsjson.String); ok {
		return strictEquals(get(value, "command"), command)
	}
	exec := get(expected, "exec")
	return exec != nil && strictEquals(get(value, "exec"), exec) &&
		jsjson.Stringify(get(value, "args")) == jsjson.Stringify(get(expected, "args"))
}

// GeneratedHookHandler is the hook entry durin writes for a harness.
func GeneratedHookHandler(input HookConfiguration) *jsjson.Object {
	timeout := int64(math.Ceil(float64(input.TimeoutMs)/1000)) + 3
	if input.Format == "copilot" {
		args := HookArguments(input.Bridge, input.Harness)
		return jsjson.Obj(
			jsjson.KV("type", jsjson.String("command")),
			jsjson.KV("exec", jsjson.String(args[0])),
			jsjson.KV("args", jsjson.Strings(args[1:]...)),
			jsjson.KV("timeoutSec", jsjson.Int(timeout)),
		)
	}
	command := jsjson.String(commandLine(input))
	switch input.Format {
	case "windsurf":
		return jsjson.Obj(jsjson.KV("command", command), jsjson.KV("show_output", jsjson.Bool(true)))
	case "cursor":
		return jsjson.Obj(jsjson.KV("command", command), jsjson.KV("timeout", jsjson.Int(timeout)), jsjson.KV("failClosed", jsjson.Bool(true)))
	case "gemini":
		return jsjson.Obj(
			jsjson.KV("type", jsjson.String("command")),
			jsjson.KV("name", jsjson.String("durin-local-check")),
			jsjson.KV("command", command),
			jsjson.KV("timeout", jsjson.Int(timeout*1000)),
		)
	}
	handler := jsjson.Obj(jsjson.KV("type", jsjson.String("command")), jsjson.KV("command", command), jsjson.KV("timeout", jsjson.Int(timeout)))
	if runtime.GOOS == "windows" && input.Format == "claude" {
		handler.Set("shell", jsjson.String("powershell"))
	}
	return handler
}

func hookEvents(format string) []string {
	switch format {
	case "windsurf":
		return []string{"pre_mcp_tool_use", "pre_run_command", "pre_read_code", "pre_write_code"}
	case "cursor":
		return []string{"preToolUse"}
	case "gemini":
		return []string{"BeforeTool"}
	}
	return []string{"PreToolUse"}
}

func commandDescriptor(args []string) *jsjson.Object {
	return jsjson.Obj(jsjson.KV("command", jsjson.Strings(args...)))
}

// legacyCustomDescriptor reports whether durin is the descriptor the TypeScript CLI wrote:
// {command: [<node>, <config>/runtime/cli.mjs, hooks, run, ...]}.
func legacyCustomDescriptor(durin jsjson.Value, input HookConfiguration) bool {
	o, ok := durin.(*jsjson.Object)
	if !ok {
		return false
	}
	command, _ := o.GetArray("command")
	if len(command) == 0 {
		return false
	}
	node, ok := command[0].(jsjson.String)
	if !ok || !isAbsoluteHere(string(node)) {
		return false
	}
	return jsjson.Stringify(durin) == jsjson.Stringify(commandDescriptor(legacyHookArguments(string(node), input.Bridge, input.Harness)))
}

func customConfig(raw string, config *jsjson.Object, input HookConfiguration) (string, error) {
	descriptor := commandDescriptor(HookArguments(input.Bridge, input.Harness))
	if durin := get(config, "durin"); durin != nil && jsjson.Stringify(durin) != jsjson.Stringify(descriptor) && !legacyCustomDescriptor(durin, input) {
		return "", clierror.New("Conflicting custom Durin hook configuration. Preserve it and review the descriptor.")
	}
	return jsoncEdit(raw, []any{"durin"}, descriptor)
}

func copilotConfig(raw string, config *jsjson.Object, input HookConfiguration) (string, error) {
	hooks, err := parseRecord(orDefault(get(config, "hooks"), jsjson.NewObject()))
	if err != nil {
		return "", err
	}
	existing, err := parseRecords(orDefault(get(hooks, "preToolUse"), jsjson.Array{}))
	if err != nil {
		return "", err
	}
	current, err := versioned(raw, config)
	if err != nil {
		return "", err
	}
	return upsertHandler(current, "preToolUse", existing, input)
}

func versioned(raw string, config *jsjson.Object) (string, error) {
	if version := get(config, "version"); version != nil && !jsjson.Equal(version, jsjson.Number(1)) {
		return "", clierror.New("Unsupported hooks configuration version.")
	}
	return jsoncEdit(raw, []any{"version"}, jsjson.Number(1))
}
