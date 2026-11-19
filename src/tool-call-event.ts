import { z } from 'zod';

/** Harnesses whose post-tool payload names the MCP server and tool reliably. */
export const REPORT_FORMATS = ['claude', 'codex', 'cursor', 'windsurf'] as const;
export type ReportFormat = (typeof REPORT_FORMATS)[number];
export const isReportFormat = (value: string): value is ReportFormat =>
  (REPORT_FORMATS as readonly string[]).includes(value);

/** A server and tool a call may have targeted; the harness configuration decides which. */
export type McpTarget = Readonly<{ server: string; tool: string }>;
export type ObservedCall = Readonly<{
  targets: readonly McpTarget[];
  input: unknown;
  outcome: 'succeeded' | 'failed' | 'unknown';
  sessionId?: string | undefined;
  turnId?: string | undefined;
  toolCallId?: string | undefined;
  cwd?: string | undefined;
}>;

const text = z.string().min(1).max(4096).optional();
const standardSchema = z.object({
  hook_event_name: z.string().optional(),
  session_id: text,
  turn_id: text,
  prompt_id: text,
  tool_use_id: text,
  tool_name: z.string().max(512),
  tool_input: z.unknown(),
  tool_response: z.unknown(),
  cwd: text,
});
const cursorSchema = z.object({
  conversation_id: text,
  generation_id: text,
  tool_name: z.string().max(512),
  tool_input: z.unknown(),
  mcp_server_name: z.string().min(1).max(512),
  result_json: z.string().optional(),
  workspace_roots: z.array(z.string()).optional(),
});
const windsurfSchema = z.object({
  agent_action_name: z.literal('post_mcp_tool_use'),
  trajectory_id: text,
  execution_id: text,
  tool_info: z.object({
    mcp_server_name: z.string().min(1).max(512),
    mcp_tool_name: z.string().min(1).max(512),
    mcp_tool_arguments: z.unknown(),
    cwd: text,
  }),
});

/**
 * Claude Code and Codex name MCP tools `mcp__<server>__<tool>`. A server or tool name
 * may itself contain `__`, so every split is a candidate, shortest server first.
 */
export function mcpTargets(name: string): McpTarget[] {
  if (!name.startsWith('mcp__')) return [];
  const parts = name.slice(5).split('__');
  return parts
    .slice(1)
    .map((_, index) => ({
      server: parts.slice(0, index + 1).join('__'),
      tool: parts.slice(index + 1).join('__'),
    }))
    .filter((target) => target.server !== '' && target.tool !== '');
}
const decoded = (value: unknown): unknown => {
  if (typeof value !== 'string') return value;
  try {
    return JSON.parse(value) as unknown;
  } catch {
    return value;
  }
};
const reportedError = (value: unknown): boolean =>
  z.object({ isError: z.literal(true) }).safeParse(decoded(value)).success;

type StandardEvent = z.infer<typeof standardSchema>;
const outcome = (failed: boolean): ObservedCall['outcome'] => (failed ? 'failed' : 'succeeded');
const standardOutcome = (event: StandardEvent) =>
  outcome(event.hook_event_name === 'PostToolUseFailure' || reportedError(event.tool_response));
// Codex sends its turn as turn_id; Claude Code's closest equivalent is the prompt ID.
const TURN_FIELD = { claude: 'prompt_id', codex: 'turn_id' } as const;
function standard(raw: unknown, format: 'claude' | 'codex'): ObservedCall | null {
  const event = standardSchema.parse(raw);
  const targets = mcpTargets(event.tool_name);
  if (targets.length === 0) return null;
  return {
    targets,
    input: event.tool_input,
    outcome: standardOutcome(event),
    sessionId: event.session_id,
    turnId: event[TURN_FIELD[format]],
    toolCallId: event.tool_use_id,
    cwd: event.cwd,
  };
}
function cursor(raw: unknown): ObservedCall {
  const event = cursorSchema.parse(raw);
  return {
    targets: [{ server: event.mcp_server_name, tool: event.tool_name.replace(/^MCP:/u, '') }],
    input: decoded(event.tool_input),
    outcome: outcome(reportedError(event.result_json)),
    sessionId: event.conversation_id,
    turnId: event.generation_id,
    cwd: event.workspace_roots?.[0],
  };
}
function windsurf(raw: unknown): ObservedCall {
  const event = windsurfSchema.parse(raw);
  return {
    targets: [{ server: event.tool_info.mcp_server_name, tool: event.tool_info.mcp_tool_name }],
    input: decoded(event.tool_info.mcp_tool_arguments),
    // Windsurf's post_mcp_tool_use payload does not say whether the tool failed.
    outcome: 'unknown',
    sessionId: event.trajectory_id,
    turnId: event.execution_id,
    cwd: event.tool_info.cwd,
  };
}
const NORMALIZERS: Readonly<Record<ReportFormat, (raw: unknown) => ObservedCall | null>> = {
  claude: (raw) => standard(raw, 'claude'),
  codex: (raw) => standard(raw, 'codex'),
  cursor,
  windsurf,
};

/** Returns the MCP call a post-tool event describes, or null for any other tool. */
export function observedMcpCall(format: ReportFormat, raw: unknown): ObservedCall | null {
  return NORMALIZERS[format](raw);
}
