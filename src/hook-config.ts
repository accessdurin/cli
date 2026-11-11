import { applyEdits, modify } from 'jsonc-parser';
import { z } from 'zod';
import type { HookFormat } from './agents.js';
import { parseConfig } from './config.js';
import type { BridgeConfiguration } from './bridge-config.js';
import { CliError } from './cli-options.js';

const object = z.record(z.string(), z.unknown());
const array = z.array(object);
const grouped = (format: HookFormat) => ['claude', 'codex', 'gemini'].includes(format);
const shellQuote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
const powershellQuote = (value: string) => `'${value.replaceAll("'", "''")}'`;
export const hookArguments = (bridge: BridgeConfiguration, harness: string) => [
  bridge.nodePath,
  bridge.cliPath,
  'hooks',
  'run',
  '--harness',
  harness,
  '--config-dir',
  bridge.configDirectory,
];
export type HookConfiguration = {
  format: HookFormat;
  bridge: BridgeConfiguration;
  harness: string;
  timeoutMs: number;
  windows?: boolean;
  previousHandler?: Record<string, unknown>;
};
function commandLine(input: HookConfiguration): string {
  const args = hookArguments(input.bridge, input.harness);
  if (!(input.windows ?? process.platform === 'win32')) return args.map(shellQuote).join(' ');
  const script = `& ${args.map(powershellQuote).join(' ')}; exit $LASTEXITCODE`;
  return `powershell.exe -NoProfile -NonInteractive -EncodedCommand ${Buffer.from(script, 'utf16le').toString('base64')}`;
}
export function updatedHookConfig(raw: string, input: HookConfiguration): string {
  const config = parseConfig(raw, { format: 'jsonc' } as import('./agents.js').AgentConfig);
  assertHooksEnabled(config);
  const args = hookArguments(input.bridge, input.harness);
  if (input.format === 'custom') return customConfig(raw, config, args);
  if (input.format === 'copilot') return copilotConfig(raw, config, input);
  return updateEvents(raw, config, input);
}
function assertHooksEnabled(config: Record<string, unknown>) {
  const hooksConfig = object.parse(config.hooksConfig ?? {});
  if (config.disableAllHooks === true || hooksConfig.enabled === false)
    throw new CliError(
      'Hooks are disabled in this harness. Review its settings before configuring local checks.',
    );
}
function updateEvents(raw: string, config: Record<string, unknown>, input: HookConfiguration) {
  const hooks = object.parse(config.hooks ?? {});
  const initial = input.format === 'cursor' ? versioned(raw, config) : raw;
  return hookEvents(input.format).reduce((current, event) => {
    const groups = array.parse(hooks[event] ?? []);
    return upsertHandler(current, event, groups, input);
  }, initial);
}
function upsertHandler(
  raw: string,
  event: string,
  groups: Record<string, unknown>[],
  input: HookConfiguration,
) {
  const handler = generatedHookHandler(input);
  const location = handlerLocation(groups, input, handler);
  if (location) return edit(raw, ['hooks', event, ...location], handler);
  const entry = grouped(input.format) ? { matcher: '.*', hooks: [handler] } : handler;
  // Appending to the syntax tree keeps comments inside every existing handler.
  return edit(raw, ['hooks', event, groups.length], entry);
}
function handlerLocation(
  groups: Record<string, unknown>[],
  input: HookConfiguration,
  handler: Record<string, unknown>,
): (string | number)[] | null {
  const groupIndex = groups.findIndex((group) =>
    grouped(input.format)
      ? array.parse(group.hooks).some((value) => sameHandler(value, handler, input.previousHandler))
      : sameHandler(group, handler, input.previousHandler),
  );
  if (groupIndex < 0) return null;
  if (!grouped(input.format)) return [groupIndex];
  const handlers = array.parse(groups[groupIndex]?.hooks);
  return [
    groupIndex,
    'hooks',
    handlers.findIndex((value) => sameHandler(value, handler, input.previousHandler)),
  ];
}
function sameHandler(
  value: Record<string, unknown>,
  expected: Record<string, unknown>,
  previous?: Record<string, unknown>,
): boolean {
  return sameInvocation(value, expected) || sameInvocation(value, previous ?? {});
}
function sameInvocation(
  value: Record<string, unknown>,
  expected: Record<string, unknown>,
): boolean {
  if (typeof expected.command === 'string') return value.command === expected.command;
  return (
    expected.exec !== undefined &&
    value.exec === expected.exec &&
    JSON.stringify(value.args) === JSON.stringify(expected.args)
  );
}
export function generatedHookHandler(input: HookConfiguration): Record<string, unknown> {
  const args = hookArguments(input.bridge, input.harness);
  if (input.format === 'copilot')
    return {
      type: 'command',
      exec: args[0],
      args: args.slice(1),
      timeoutSec: Math.ceil(input.timeoutMs / 1000) + 3,
    };
  return hookHandler(input, commandLine(input));
}
function hookEvents(format: HookFormat): string[] {
  const events: Partial<Record<HookFormat, string[]>> = {
    windsurf: ['pre_mcp_tool_use', 'pre_run_command', 'pre_read_code', 'pre_write_code'],
    cursor: ['preToolUse'],
    gemini: ['BeforeTool'],
  };
  return events[format] ?? ['PreToolUse'];
}
function hookHandler(input: HookConfiguration, command: string) {
  const timeout = Math.ceil(input.timeoutMs / 1000) + 3;
  if (input.format === 'windsurf') return { command, show_output: true };
  if (input.format === 'cursor') return { command, timeout, failClosed: true };
  return commandHandler(input.format, command, timeout);
}
function commandHandler(format: HookFormat, command: string, timeout: number) {
  if (format === 'gemini')
    return { type: 'command', name: 'durin-local-check', command, timeout: timeout * 1000 };
  return {
    type: 'command',
    command,
    timeout,
    ...(process.platform === 'win32' && format === 'claude' ? { shell: 'powershell' } : {}),
  };
}
function customConfig(raw: string, config: Record<string, unknown>, args: string[]) {
  if (
    config.durin !== undefined &&
    JSON.stringify(config.durin) !== JSON.stringify({ command: args })
  )
    throw new CliError(
      'Conflicting custom Durin hook configuration. Preserve it and review the descriptor.',
    );
  return edit(raw, ['durin'], { command: args });
}
function copilotConfig(raw: string, config: Record<string, unknown>, input: HookConfiguration) {
  const hooks = object.parse(config.hooks ?? {});
  const existing = array.parse(hooks.preToolUse ?? []);
  return upsertHandler(versioned(raw, config), 'preToolUse', existing, input);
}
function versioned(raw: string, config: Record<string, unknown>) {
  if (config.version !== undefined && config.version !== 1)
    throw new CliError('Unsupported hooks configuration version.');
  return edit(raw, ['version'], 1);
}
const edit = (raw: string, path: (string | number)[], value: unknown) =>
  applyEdits(
    raw,
    modify(raw, path, value, {
      formattingOptions: { insertSpaces: true, tabSize: 2 },
    }),
  );
