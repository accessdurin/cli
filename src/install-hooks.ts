import { mkdir, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import type { AgentConfig } from './agents.js';
import type { BridgeConfiguration } from './bridge-config.js';
import type { HookChecker } from './hook-checker.js';
import { validateChecker } from './hook-checker.js';
import { generatedHookHandler, hookArguments, updatedHookConfig } from './hook-config.js';
import {
  hookInstallationPath,
  hookInstallationSchema,
  type HookInstallation,
} from './hook-runner.js';
import { regularText, saveFileChange, planManagedFile, textDigest } from './managed-files.js';
import { CliError, type CliOptions } from './cli-options.js';

export async function installHooks(
  agent: AgentConfig,
  bridge: BridgeConfiguration,
  checker: HookChecker,
): Promise<void> {
  if (!agent.id || !agent.hookConfig)
    throw new CliError('This harness has no configured hook adapter.');
  await validateChecker(checker);
  const installation = {
    format: agent.hookConfig.format,
    harness: agent.id,
    checker,
    target: agent.hookConfig.path,
    installedAt: new Date().toISOString(),
  };
  const path = hookInstallationPath(bridge.configDirectory, agent.id);
  const previous = await regularText(path);
  const previousHandler = previousHandlerFrom(previous);
  const before = await regularText(installation.target);
  const after = hookContent(installation, emptyConfig(before), bridge, previousHandler);
  const change =
    installation.format === 'opencode'
      ? await planManagedFile(installation.target, after, pluginDigest(previous))
      : { path: installation.target, before, after };
  // Record ownership only after the harness configuration was saved successfully.
  const handler = generatedHookHandler({
    format: installation.format,
    bridge,
    harness: installation.harness,
    timeoutMs: checker.timeoutMs,
  });
  const saved =
    JSON.stringify({ ...installation, handler, assetDigest: textDigest(after) }, null, 2) + '\n';
  await saveFileChange(change);
  await saveFileChange({ path, before: previous, after: saved });
}
const previousHandlerFrom = (raw: string | null) =>
  raw === null ? undefined : hookInstallationSchema.parse(JSON.parse(raw)).handler;
const emptyConfig = (raw: string | null) => raw ?? '{}';
function pluginDigest(previous: string | null): string | undefined {
  if (!previous) return undefined;
  return (JSON.parse(previous) as { assetDigest?: string }).assetDigest;
}
function hookContent(
  installation: Omit<HookInstallation, 'assetDigest'>,
  raw: string,
  bridge: BridgeConfiguration,
  previousHandler?: Record<string, unknown>,
) {
  if (installation.format === 'opencode') return openCodePlugin(bridge, installation);
  return updatedHookConfig(raw, {
    format: installation.format,
    harness: installation.harness,
    bridge,
    timeoutMs: installation.checker.timeoutMs,
    ...(previousHandler ? { previousHandler } : {}),
  });
}
function openCodePlugin(
  bridge: BridgeConfiguration,
  installation: Omit<HookInstallation, 'assetDigest'>,
): string {
  const [command, ...args] = hookArguments(bridge, installation.harness);
  return `// Managed by Durin CLI. Compatible with OpenCode V2 and V1 >= 1.18.29.
import { execFile } from 'node:child_process';
const check = (event) => new Promise((resolve, reject) => {
  const child = execFile(${JSON.stringify(command)}, ${JSON.stringify(args)}, { timeout: ${installation.checker.timeoutMs + 3000}, maxBuffer: 65536, windowsHide: true }, (error) => error ? reject(new Error('Durin local check denied or failed.')) : resolve());
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
`;
}
export async function hookCommand(options: CliOptions): Promise<void> {
  const action = options.positionals[1];
  if (action !== 'status')
    throw new CliError('Use durin hooks status, or run durin to configure hooks.');
  const directory = join(options.directory, 'hooks');
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const names = (await readdir(directory)).filter((name) => /^[a-z][a-z0-9-]*\.json$/u.test(name));
  const results = await Promise.all(names.map((name) => hookStatus(join(directory, name))));
  process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
}
async function hookStatus(path: string) {
  const raw = await regularText(path);
  const entry = hookInstallationSchema.parse(JSON.parse(raw ?? '{}'));
  const config = await regularText(entry.target);
  const checker = await validateChecker(entry.checker).then(
    () => 'available',
    () => 'unavailable',
  );
  return {
    harness: entry.harness,
    target: entry.target,
    checker,
    configuration:
      config !== null && textDigest(config) === entry.assetDigest
        ? 'installed'
        : 'missing-or-changed',
    execution: 'not-verified',
  };
}
