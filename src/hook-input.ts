import { createHash, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { z } from 'zod';
import type { HookFormat } from './agents.js';
import { CliError } from './cli-options.js';

const object = z.record(z.string(), z.unknown());
const identifier = z.string().min(1).max(4096);
export const hookInputSchema = z.object({
  hook_event_name: z.literal('PreToolUse'),
  session_id: identifier,
  tool_use_id: identifier,
  tool_name: identifier,
  tool_input: object,
  cwd: z.string().optional(),
  permission_mode: z.string().optional(),
  model: z.string().optional(),
  turn_id: z.string().optional(),
});
export type HookInput = z.infer<typeof hookInputSchema>;
const argumentsObject = (value: unknown): unknown =>
  typeof value === 'string' ? JSON.parse(value) : value;
const stringValue = (value: unknown): string | undefined =>
  typeof value === 'string' ? value : undefined;

export function normalizeHook(value: unknown, format: HookFormat): HookInput {
  const raw = object.parse(value);
  const adapted = adaptedInput(raw, format);
  const result = hookInputSchema.safeParse(adapted);
  if (!result.success) throw new CliError('Invalid local hook input; the checker was not invoked.');
  return result.data;
}
function adaptedInput(raw: Record<string, unknown>, format: HookFormat) {
  if (format === 'windsurf') return windsurfInput(raw);
  if (format === 'copilot') return copilotInput(raw);
  return standardInput(raw, format);
}
function standardInput(raw: Record<string, unknown>, format: HookFormat) {
  const adapted = ['cursor', 'opencode', 'gemini'].includes(format);
  return {
    ...raw,
    hook_event_name: adapted ? 'PreToolUse' : raw.hook_event_name,
    session_id: raw.session_id ?? raw.conversation_id,
    tool_use_id: raw.tool_use_id ?? missingCallId(format),
    tool_input: argumentsObject(raw.tool_input),
  };
}
const missingCallId = (format: HookFormat) => (format === 'gemini' ? randomUUID() : undefined);
function copilotInput(raw: Record<string, unknown>) {
  return {
    hook_event_name: 'PreToolUse',
    session_id: raw.sessionId,
    tool_use_id: raw.toolCallId ?? randomUUID(),
    tool_name: raw.toolName,
    tool_input: argumentsObject(raw.toolArgs),
    cwd: raw.cwd,
  };
}
const optionalId = (value: unknown) => stringValue(value) ?? randomUUID();
// Windsurf sends `trajectory_id` for the whole Cascade conversation. Without it,
// fall back to the spawning Windsurf process so calls still group per session.
const windsurfSessionId = (raw: Record<string, unknown>) =>
  stringValue(raw.session_id) ?? stringValue(raw.trajectory_id) ?? synthesizedSessionId();
let synthesized: string | undefined;
function synthesizedSessionId() {
  synthesized ??= `windsurf-synth-${createHash('sha256').update(parentProcessKey()).digest('hex')}`;
  return synthesized;
}
function parentProcessKey() {
  const parent = String(process.ppid);
  try {
    // Linux only: boot ID plus the parent's start time (field 22) keeps a reused PID distinct.
    const stat = readFileSync(`/proc/${parent}/stat`, 'utf8');
    const started = stat.slice(stat.lastIndexOf(')') + 2).split(' ')[19];
    const boot = readFileSync('/proc/sys/kernel/random/boot_id', 'utf8').trim();
    return started ? `${parent}:${started}:${boot}` : parent;
  } catch {
    return parent;
  }
}
function windsurfInput(raw: Record<string, unknown>) {
  const info = object.parse(raw.tool_info);
  const names: Record<string, string> = {
    pre_run_command: 'Bash',
    pre_read_code: 'Read',
    pre_write_code: 'Write',
  };
  const action = identifier.parse(raw.agent_action_name);
  if (!['pre_mcp_tool_use', ...Object.keys(names)].includes(action))
    throw new CliError('Unsupported Windsurf hook event.');
  const tool =
    action === 'pre_mcp_tool_use'
      ? `mcp__${identifier.parse(info.mcp_server_name)}__${identifier.parse(info.mcp_tool_name)}`
      : names[action];
  return {
    hook_event_name: 'PreToolUse',
    session_id: windsurfSessionId(raw),
    tool_use_id: optionalId(raw.tool_use_id),
    tool_name: tool,
    tool_input:
      action === 'pre_mcp_tool_use'
        ? argumentsObject(info.mcp_tool_arguments)
        : shellArguments(info),
    cwd: stringValue(info.cwd),
  };
}
const shellArguments = (info: Record<string, unknown>) => ({
  ...info,
  ...(info.command_line === undefined ? {} : { command: info.command_line }),
});
