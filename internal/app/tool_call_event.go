package app

import (
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// ReportFormats are the harnesses whose post-tool payload names the MCP server and tool
// reliably (src/tool-call-event.ts).
var ReportFormats = []string{"claude", "codex", "cursor", "windsurf"}

func isReportFormat(value string) bool {
	for _, f := range ReportFormats {
		if f == value {
			return true
		}
	}
	return false
}

// McpTarget is a server and tool a call may have targeted; the harness configuration
// decides which.
type McpTarget struct{ Server, Tool string }

// ObservedCall is the MCP call a post-tool event describes. Empty identifiers are absent.
type ObservedCall struct {
	Targets    []McpTarget
	Input      jsjson.Value // nil when the event has no arguments
	Outcome    string       // succeeded | failed | unknown
	SessionID  string
	TurnID     string
	ToolCallID string
	Cwd        string
}

var (
	eventText      = zod.Optional(zod.String().Min(1).Max(4096))
	eventToolName  = zod.String().Max(512)
	standardSchema = zod.Object(
		zod.F("hook_event_name", zod.Optional(zod.String())),
		zod.F("session_id", eventText),
		zod.F("turn_id", eventText),
		zod.F("prompt_id", eventText),
		zod.F("tool_use_id", eventText),
		zod.F("tool_name", eventToolName),
		zod.F("tool_input", zod.Unknown()),
		zod.F("tool_response", zod.Unknown()),
		zod.F("cwd", eventText),
	)
	cursorSchema = zod.Object(
		zod.F("conversation_id", eventText),
		zod.F("generation_id", eventText),
		zod.F("tool_name", eventToolName),
		zod.F("tool_input", zod.Unknown()),
		zod.F("mcp_server_name", zod.String().Min(1).Max(512)),
		zod.F("result_json", zod.Optional(zod.String())),
		zod.F("workspace_roots", zod.Optional(zod.Array(zod.String()))),
	)
	windsurfSchema = zod.Object(
		zod.F("agent_action_name", zod.Literal(jsjson.String("post_mcp_tool_use"))),
		zod.F("trajectory_id", eventText),
		zod.F("execution_id", eventText),
		zod.F("tool_info", zod.Object(
			zod.F("mcp_server_name", zod.String().Min(1).Max(512)),
			zod.F("mcp_tool_name", zod.String().Min(1).Max(512)),
			zod.F("mcp_tool_arguments", zod.Unknown()),
			zod.F("cwd", eventText),
		)),
	)
	reportedErrorSchema = zod.Object(zod.F("isError", zod.Literal(jsjson.Bool(true))))
)

// McpTargets splits a Claude Code or Codex tool name, mcp__<server>__<tool>. A server or
// tool name may itself contain __, so every split is a candidate, shortest server first.
func McpTargets(name string) []McpTarget {
	if !strings.HasPrefix(name, "mcp__") {
		return nil
	}
	parts := strings.Split(name[len("mcp__"):], "__")
	var out []McpTarget
	for i := 1; i < len(parts); i++ {
		target := McpTarget{Server: strings.Join(parts[:i], "__"), Tool: strings.Join(parts[i:], "__")}
		if target.Server != "" && target.Tool != "" {
			out = append(out, target)
		}
	}
	return out
}

// decoded is a JSON-encoded string's value, or the value itself.
func decoded(value jsjson.Value) jsjson.Value {
	s, ok := value.(jsjson.String)
	if !ok {
		return value
	}
	parsed, err := jsjson.ParseString(string(s))
	if err != nil {
		return value
	}
	return parsed
}

func reportedError(value jsjson.Value) bool { return zod.Valid(reportedErrorSchema, decoded(value)) }

func outcomeOf(failed bool) string {
	if failed {
		return "failed"
	}
	return "succeeded"
}

func eventString(o *jsjson.Object, key string) string { s, _ := o.GetString(key); return s }

func parseEvent(schema zod.Schema, raw jsjson.Value) (*jsjson.Object, error) {
	out, err := zod.Parse(schema, raw)
	if err != nil {
		return nil, err
	}
	return out.(*jsjson.Object), nil
}

// standardCall reads Claude Code and Codex. Codex sends its turn as turn_id; Claude Code's
// closest equivalent is the prompt ID.
func standardCall(raw jsjson.Value, format string) (*ObservedCall, error) {
	event, err := parseEvent(standardSchema, raw)
	if err != nil {
		return nil, err
	}
	targets := McpTargets(eventString(event, "tool_name"))
	if len(targets) == 0 {
		return nil, nil
	}
	turn := "prompt_id"
	if format == "codex" {
		turn = "turn_id"
	}
	return &ObservedCall{
		Targets:    targets,
		Input:      get(event, "tool_input"),
		Outcome:    outcomeOf(eventString(event, "hook_event_name") == "PostToolUseFailure" || reportedError(get(event, "tool_response"))),
		SessionID:  eventString(event, "session_id"),
		TurnID:     eventString(event, turn),
		ToolCallID: eventString(event, "tool_use_id"),
		Cwd:        eventString(event, "cwd"),
	}, nil
}

func cursorCall(raw jsjson.Value) (*ObservedCall, error) {
	event, err := parseEvent(cursorSchema, raw)
	if err != nil {
		return nil, err
	}
	cwd := ""
	if roots, ok := event.GetArray("workspace_roots"); ok && len(roots) > 0 {
		cwd = string(roots[0].(jsjson.String))
	}
	return &ObservedCall{
		Targets:   []McpTarget{{Server: eventString(event, "mcp_server_name"), Tool: strings.TrimPrefix(eventString(event, "tool_name"), "MCP:")}},
		Input:     decoded(get(event, "tool_input")),
		Outcome:   outcomeOf(reportedError(get(event, "result_json"))),
		SessionID: eventString(event, "conversation_id"),
		TurnID:    eventString(event, "generation_id"),
		Cwd:       cwd,
	}, nil
}

// windsurfCall reads Windsurf. Its post_mcp_tool_use payload does not say whether the tool
// failed.
func windsurfCall(raw jsjson.Value) (*ObservedCall, error) {
	event, err := parseEvent(windsurfSchema, raw)
	if err != nil {
		return nil, err
	}
	info, _ := event.GetObject("tool_info")
	return &ObservedCall{
		Targets:   []McpTarget{{Server: eventString(info, "mcp_server_name"), Tool: eventString(info, "mcp_tool_name")}},
		Input:     decoded(get(info, "mcp_tool_arguments")),
		Outcome:   "unknown",
		SessionID: eventString(event, "trajectory_id"),
		TurnID:    eventString(event, "execution_id"),
		Cwd:       eventString(info, "cwd"),
	}, nil
}

// ObservedMcpCall is the MCP call a post-tool event describes; nil for any other tool.
func ObservedMcpCall(format string, raw jsjson.Value) (*ObservedCall, error) {
	switch format {
	case "claude", "codex":
		return standardCall(raw, format)
	case "cursor":
		return cursorCall(raw)
	case "windsurf":
		return windsurfCall(raw)
	}
	return nil, nil
}
