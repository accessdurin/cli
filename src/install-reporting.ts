import { z } from 'zod';
import type { AgentConfig } from './agents.js';
import type { BridgeConfiguration } from './bridge-config.js';
import { parseConfig } from './config.js';
import { assertHooksEnabled, editJsonc, harnessCommandLine } from './hook-config.js';
import { regularText, saveFileChange } from './managed-files.js';
import { reportingPath } from './report-hook.js';
import { isReportFormat, type ReportFormat } from './tool-call-event.js';
import { CliError } from './cli-options.js';

const object = z.record(z.string(), z.unknown());
const array = z.array(object);
type Plan = Readonly<{
  events: readonly string[];
  grouped: boolean;
  handler: (command: string) => Record<string, unknown>;
}>;
// Claude Code and Codex run the report in the background; Cursor and Windsurf have no
// background option, so the report command itself returns before any network call.
const asyncCommand = (command: string) => ({ type: 'command', command, async: true, timeout: 30 });
const PLANS: Readonly<Record<ReportFormat, Plan>> = {
  claude: { events: ['PostToolUse', 'PostToolUseFailure'], grouped: true, handler: asyncCommand },
  codex: { events: ['PostToolUse'], grouped: true, handler: asyncCommand },
  cursor: {
    events: ['afterMCPExecution'],
    grouped: false,
    handler: (command) => ({ command, timeout: 10 }),
  },
  windsurf: {
    events: ['post_mcp_tool_use'],
    grouped: false,
    handler: (command) => ({ command, show_output: false }),
  },
};
export const reportArguments = (bridge: BridgeConfiguration, harness: string) => [
  bridge.nodePath,
  bridge.cliPath,
  'hooks',
  'report',
  '--harness',
  harness,
  '--config-dir',
  bridge.configDirectory,
];

const hasCommand = (entry: Record<string, unknown>, command: string, grouped: boolean) =>
  grouped
    ? array.safeParse(entry.hooks).data?.some((hook) => hook.command === command) === true
    : entry.command === command;
const jsonc = (raw: string) => parseConfig(raw, { format: 'jsonc' } as AgentConfig);
const eventEntries = (raw: string, event: string) =>
  array.parse(object.parse(jsonc(raw).hooks ?? {})[event] ?? []);
const reportEntry = (plan: Plan, command: string) =>
  plan.grouped ? { matcher: 'mcp__.*', hooks: [plan.handler(command)] } : plan.handler(command);
function withEvent(raw: string, event: string, plan: Plan, command: string): string {
  const entries = eventEntries(raw, event);
  if (entries.some((entry) => hasCommand(entry, command, plan.grouped))) return raw;
  return editJsonc(raw, ['hooks', event, entries.length], reportEntry(plan, command));
}
/** Adds Durin's post-tool report handler to a harness hook file, keeping every other hook. */
export function updatedReportConfig(raw: string, format: ReportFormat, command: string): string {
  const config = jsonc(raw);
  assertHooksEnabled(config);
  const plan = PLANS[format];
  const versioned =
    format === 'cursor' && config.version === undefined ? editJsonc(raw, ['version'], 1) : raw;
  return plan.events.reduce((text, event) => withEvent(text, event, plan, command), versioned);
}

/**
 * Configures the harness to report calls to local MCP servers to this profile's
 * organization. The hook itself stays quiet while Durin Endpoint covers the harness.
 */
export async function installReporting(
  agent: AgentConfig,
  bridge: BridgeConfiguration,
): Promise<void> {
  const { harness, format, target } = reportTarget(agent);
  const command = harnessCommandLine(reportArguments(bridge, harness), format);
  const before = await regularText(target);
  const after = updatedReportConfig(before ?? '{}', format, command);
  const path = reportingPath(bridge.configDirectory, harness);
  const record = {
    harness,
    format,
    profileId: bridge.profileId,
    target,
    command,
    mcpConfig: { configPath: agent.configPath, format: agent.format, serverKey: agent.serverKey },
    installedAt: new Date().toISOString(),
  };
  await saveFileChange({ path: target, before, after });
  // Record ownership only after the harness configuration was saved successfully.
  await saveFileChange({
    path,
    before: await regularText(path),
    after: `${JSON.stringify(record, null, 2)}\n`,
  });
}
export const reportingSupported = (agent: AgentConfig): boolean =>
  [
    agent.id !== undefined,
    agent.hookConfig?.format === agent.id,
    isReportFormat(agent.id ?? ''),
  ].every(Boolean);
const supportedFormat = (agent: AgentConfig): ReportFormat | null =>
  reportingSupported(agent) && isReportFormat(agent.id ?? '') ? (agent.id as ReportFormat) : null;
function reportTarget(agent: AgentConfig) {
  const format = supportedFormat(agent);
  if (!format) throw new CliError('This harness has no local tool-call reporting adapter.');
  return { harness: format, format, target: agent.hookConfig?.path ?? '' };
}
