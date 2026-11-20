package app

import (
	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

var (
	recordSchema    = zod.Record(zod.Unknown())
	hookIdentifier  = zod.String().Min(1).Max(4096)
	optionalString  = zod.Optional(zod.String())
	hookInputSchema = zod.Object(
		zod.F("hook_event_name", zod.Literal(jsjson.String("PreToolUse"))),
		zod.F("session_id", hookIdentifier),
		zod.F("tool_use_id", hookIdentifier),
		zod.F("tool_name", hookIdentifier),
		zod.F("tool_input", recordSchema),
		zod.F("cwd", optionalString),
		zod.F("permission_mode", optionalString),
		zod.F("model", optionalString),
		zod.F("turn_id", optionalString),
	)
)

// argumentsObject parses a JSON string of tool arguments; other values pass through.
func argumentsObject(value jsjson.Value) (jsjson.Value, error) {
	if s, ok := value.(jsjson.String); ok {
		return jsjson.ParseString(string(s))
	}
	return value, nil
}

func stringValue(value jsjson.Value) jsjson.Value {
	if s, ok := value.(jsjson.String); ok {
		return s
	}
	return nil
}

// nullish is JavaScript's value ?? fallback.
func nullish(value jsjson.Value, fallback func() jsjson.Value) jsjson.Value {
	if _, isNull := value.(jsjson.Null); value == nil || isNull {
		return fallback()
	}
	return value
}

func get(o *jsjson.Object, key string) jsjson.Value { v, _ := o.Get(key); return v }

func newUUID() jsjson.Value { return jsjson.String(randomUUID()) }

// NormalizeHook adapts a harness's pre-tool hook event to the PreToolUse event a checker
// receives (src/hook-input.ts): schema-ordered, unknown keys dropped.
func NormalizeHook(value jsjson.Value, format string) (*jsjson.Object, error) {
	parsed, err := zod.Parse(recordSchema, value)
	if err != nil {
		return nil, err
	}
	adapted, err := adaptedInput(parsed.(*jsjson.Object), format)
	if err != nil {
		return nil, err
	}
	out, zerr := zod.SafeParse(hookInputSchema, adapted)
	if zerr != nil {
		return nil, clierror.New("Invalid local hook input; the checker was not invoked.")
	}
	return out.(*jsjson.Object), nil
}

func adaptedInput(raw *jsjson.Object, format string) (*jsjson.Object, error) {
	switch format {
	case "windsurf":
		return windsurfInput(raw)
	case "copilot":
		return copilotInput(raw)
	}
	return standardInput(raw, format)
}

func standardInput(raw *jsjson.Object, format string) (*jsjson.Object, error) {
	adapted := format == "cursor" || format == "opencode" || format == "gemini" || format == "pi" || format == "kiro"
	out := raw.Clone()
	if adapted {
		out.Set("hook_event_name", jsjson.String("PreToolUse"))
	} else {
		out.Set("hook_event_name", get(raw, "hook_event_name"))
	}
	out.Set("session_id", nullish(get(raw, "session_id"), func() jsjson.Value { return get(raw, "conversation_id") }))
	out.Set("tool_use_id", nullish(get(raw, "tool_use_id"), func() jsjson.Value {
		if format == "gemini" || format == "kiro" || format == "pi" {
			return newUUID()
		}
		return nil
	}))
	input, err := argumentsObject(get(raw, "tool_input"))
	if err != nil {
		return nil, err
	}
	out.Set("tool_input", input)
	return out, nil
}

func copilotInput(raw *jsjson.Object) (*jsjson.Object, error) {
	out := jsjson.Obj(
		jsjson.KV("hook_event_name", jsjson.String("PreToolUse")),
		jsjson.KV("session_id", get(raw, "sessionId")),
		jsjson.KV("tool_use_id", nullish(get(raw, "toolCallId"), newUUID)),
		jsjson.KV("tool_name", get(raw, "toolName")),
	)
	input, err := argumentsObject(get(raw, "toolArgs"))
	if err != nil {
		return nil, err
	}
	out.Set("tool_input", input)
	out.Set("cwd", get(raw, "cwd"))
	return out, nil
}

var windsurfTools = map[string]string{
	"pre_run_command": "Bash",
	"pre_read_code":   "Read",
	"pre_write_code":  "Write",
}

func optionalID(value jsjson.Value) jsjson.Value {
	if s, ok := value.(jsjson.String); ok {
		return s
	}
	return newUUID()
}

func identifier(value jsjson.Value) (string, error) {
	out, err := zod.Parse(hookIdentifier, value)
	if err != nil {
		return "", err
	}
	return string(out.(jsjson.String)), nil
}

func windsurfInput(raw *jsjson.Object) (*jsjson.Object, error) {
	parsedInfo, err := zod.Parse(recordSchema, get(raw, "tool_info"))
	if err != nil {
		return nil, err
	}
	info := parsedInfo.(*jsjson.Object)
	action, err := identifier(get(raw, "agent_action_name"))
	if err != nil {
		return nil, err
	}
	tool, known := windsurfTools[action]
	if !known && action != "pre_mcp_tool_use" {
		return nil, clierror.New("Unsupported Windsurf hook event.")
	}
	if action == "pre_mcp_tool_use" {
		server, err := identifier(get(info, "mcp_server_name"))
		if err != nil {
			return nil, err
		}
		name, err := identifier(get(info, "mcp_tool_name"))
		if err != nil {
			return nil, err
		}
		tool = "mcp__" + server + "__" + name
	}
	out := jsjson.Obj(
		jsjson.KV("hook_event_name", jsjson.String("PreToolUse")),
		jsjson.KV("session_id", optionalID(get(raw, "session_id"))),
		jsjson.KV("tool_use_id", optionalID(get(raw, "tool_use_id"))),
		jsjson.KV("tool_name", jsjson.String(tool)),
	)
	var input jsjson.Value = shellArguments(info)
	if action == "pre_mcp_tool_use" {
		if input, err = argumentsObject(get(info, "mcp_tool_arguments")); err != nil {
			return nil, err
		}
	}
	out.Set("tool_input", input)
	out.Set("cwd", stringValue(get(info, "cwd")))
	return out, nil
}

func shellArguments(info *jsjson.Object) *jsjson.Object {
	out := info.Clone()
	if command, ok := info.Get("command_line"); ok && command != nil {
		out.Set("command", command)
	}
	return out
}
