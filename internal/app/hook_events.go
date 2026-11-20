package app

import "regexp"

// HookEvent is one hook event a harness exposes and durin registers a handler for.
type HookEvent struct {
	Name string
	// Check events run the selected local checker (today's pre-tool check) when one is
	// configured. Every other event only forwards its payload to the observer and continues.
	Check bool
	// Respond events require a decision from every handler (Cursor's permission hooks). A
	// capture-only handler cannot answer without overriding the harness's own approval, so
	// they are registered only as checks, when a checker is configured.
	Respond bool
	// Tool events take a tool matcher in grouped formats; others are registered without one.
	Tool bool
	// Output is what a handler prints to continue. Empty for most harnesses: exit 0 with no
	// output continues, and output on some events is added to the model's context.
	Output string
	// TimeoutSec overrides the capture handler timeout where the harness caps it.
	TimeoutSec int
}

// captureTimeoutSec is the timeout of capture handlers; they normally finish in
// milliseconds and never wait for the observer longer than captureSendBudget.
const captureTimeoutSec = 10

// hookEventPattern bounds --event values (OpenCode bus events use dots).
var hookEventPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{0,63}$`)

func events(check []string, tool []string, rest ...string) []HookEvent {
	var out []HookEvent
	for _, n := range check {
		out = append(out, HookEvent{Name: n, Check: true, Tool: true})
	}
	for _, n := range tool {
		out = append(out, HookEvent{Name: n, Tool: true})
	}
	for _, n := range rest {
		out = append(out, HookEvent{Name: n})
	}
	return out
}

// HarnessEvents are the hook events registered for each built-in hook format. Each list is
// every event the harness documents (see docs/hooks.md for the pages relied on) except:
//   - events where registering any handler changes default behaviour (Claude Code
//     WorktreeCreate and WorktreeRemove replace git worktree handling; a PreModelSwitch hook
//     that times out blocks the switch; pi's project_trust must answer);
//   - events fired for every streamed chunk (Claude Code MessageDisplay, Gemini CLI
//     AfterModel, pi message_update, provider_stream_event and tool_execution_update,
//     OpenCode message.part.updated);
//   - Cursor's permission hooks other than preToolUse, which require a decision.
var HarnessEvents = map[string][]HookEvent{
	"claude": events([]string{"PreToolUse"}, []string{"PermissionRequest", "PermissionDenied", "PostToolUse", "PostToolUseFailure"},
		"SessionStart", "Setup", "UserPromptSubmit", "UserPromptExpansion", "PostToolBatch", "Notification",
		"SubagentStart", "SubagentStop", "TaskCreated", "TaskCompleted", "Stop", "StopFailure", "TeammateIdle",
		"InstructionsLoaded", "ConfigChange", "CwdChanged", "DirectoryAdded", "FileChanged", "PreCompact",
		"PostCompact", "PostModelSwitch", "Elicitation", "ElicitationResult", "SessionEnd"),
	"codex": append(events([]string{"PreToolUse"}, []string{"PermissionRequest", "PostToolUse"},
		"PreCompact", "PostCompact", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop",
		"PreModelRequest", "PostModelRequest", "SessionStart"),
		// Codex caps these at 3 seconds.
		HookEvent{Name: "Interrupt", TimeoutSec: 3}, HookEvent{Name: "SessionEnd", TimeoutSec: 3}),
	"cursor": append([]HookEvent{{Name: "preToolUse", Check: true, Respond: true, Output: `{"permission":"allow"}`},
		{Name: "beforeSubmitPrompt", Output: `{"continue":true}`}},
		withOutput("{}", events(nil, nil, "sessionStart", "sessionEnd", "postToolUse", "postToolUseFailure",
			"subagentStop", "afterShellExecution", "afterMCPExecution", "afterFileEdit", "preCompact", "stop",
			"afterAgentResponse", "afterAgentThought", "afterTabFileEdit", "workspaceOpen"))...),
	"windsurf": events([]string{"pre_mcp_tool_use", "pre_run_command", "pre_read_code", "pre_write_code"}, nil,
		"post_read_code", "post_write_code", "post_run_command", "post_mcp_tool_use", "pre_user_prompt",
		"post_cascade_response", "post_cascade_response_with_transcript"),
	"gemini": events([]string{"BeforeTool"}, []string{"AfterTool"},
		"BeforeAgent", "AfterAgent", "BeforeModel", "BeforeToolSelection", "SessionStart", "SessionEnd",
		"Notification", "PreCompress"),
	"copilot": events([]string{"preToolUse"}, nil,
		"sessionStart", "sessionEnd", "userPromptSubmitted", "userPromptTransformed", "postToolUse",
		"postToolUseFailure", "agentStop", "subagentStart", "subagentStop", "errorOccurred", "preCompact",
		"permissionRequest", "notification"),
	// OpenCode's plugin forwards every bus event under its own type, except streamed parts.
	"opencode": events([]string{"PreToolUse"}, nil, "tool.execute.after", "chat.message"),
	"pi": events([]string{"tool_call"}, nil,
		"resources_discover", "mcp_servers_change", "session_start", "session_info_changed",
		"session_before_switch", "session_before_fork", "session_before_compact", "session_compact",
		"session_compact_failed", "session_before_tree", "session_tree", "session_shutdown",
		"before_agent_start", "agent_start", "agent_end", "agent_before_settle", "agent_settled",
		"ui_prompt_start", "ui_prompt_end", "turn_start", "turn_end", "message_start", "message_end",
		"context", "context_with_system", "before_provider_headers", "before_provider_request",
		"after_provider_response", "cache_warming_decision", "tool_execution_start", "tool_execution_end",
		"tool_result", "model_select", "thinking_level_select", "input", "user_bash"),
	"kiro": events([]string{"PreToolUse"}, []string{"PostToolUse"},
		"SessionStart", "Stop", "UserPromptSubmit", "PreTaskExec", "PostTaskExec", "PostFileCreate",
		"PostFileSave", "PostFileDelete"),
}

func withOutput(output string, in []HookEvent) []HookEvent {
	for i := range in {
		in[i].Output = output
	}
	return in
}

// lookupHookEvent finds an event of a format. OpenCode bus events are not listed; any
// valid name is a capture event there.
func lookupHookEvent(format, name string) (HookEvent, bool) {
	for _, e := range HarnessEvents[format] {
		if e.Name == name {
			return e, true
		}
	}
	return HookEvent{}, false
}

// registeredEvents are the events durin installs handlers for: every event, except
// Respond events when there is no checker to answer them.
func registeredEvents(format string, checker bool) []HookEvent {
	var out []HookEvent
	for _, e := range HarnessEvents[format] {
		if e.Respond && !checker {
			continue
		}
		out = append(out, e)
	}
	return out
}
