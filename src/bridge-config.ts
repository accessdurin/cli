import { CliError } from './cli-options.js';
import { isAbsolute } from 'node:path';
import { applyEdits, modify } from 'jsonc-parser';
import { stringify } from 'smol-toml';
import { z } from 'zod';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import type { AgentConfig } from './agents.js';
import { parseConfig, serverFrom, writeAgentConfig } from './config.js';

const absolutePath = z
  .string()
  .min(1)
  .refine(isAbsolute)
  .refine((value) => !/[\r\n\0]/u.test(value));
const bridgeSchema = z
  .object({
    nodePath: absolutePath,
    cliPath: absolutePath,
    configDirectory: absolutePath,
    profileId: cliProfileIdSchema,
  })
  .strict();
export type BridgeConfiguration = z.infer<typeof bridgeSchema>;
export const bridgeServerName = (profileId: string): string =>
  `durin-${cliProfileIdSchema.parse(profileId).slice(0, 12)}`;

const bridgeEntry = (agent: AgentConfig, input: BridgeConfiguration) => {
  const args = [
    input.cliPath,
    'mcp',
    'serve',
    '--profile',
    input.profileId,
    '--config-dir',
    input.configDirectory,
  ];
  const transport =
    agent.bridge === 'command-array'
      ? { command: [input.nodePath, ...args] }
      : { command: input.nodePath, args };
  return { ...agent.bridgeTransport, ...transport };
};
const sameEntry = (existing: Record<string, unknown>, expected: Record<string, unknown>): boolean =>
  [
    existing.disabled !== true,
    existing.enabled !== false,
    existing.url === undefined,
    existing.serverUrl === undefined,
    Object.entries(expected).every(
      ([key, value]) => JSON.stringify(existing[key]) === JSON.stringify(value),
    ),
  ].every(Boolean);
const assertCompatible = (
  existing: Record<string, unknown> | null,
  entry: Record<string, unknown>,
): void => {
  if (existing && !sameEntry(existing, entry))
    throw new CliError(
      'This organization already has a conflicting agent configuration. Review it before running durin again.',
    );
};

export function updatedBridgeConfig(
  raw: string,
  agent: AgentConfig,
  input: BridgeConfiguration,
): string {
  const parsed = bridgeSchema.parse(input);
  const name = bridgeServerName(parsed.profileId);
  const entry = bridgeEntry(agent, parsed);
  const existing = serverFrom(parseConfig(raw, agent), agent, name);
  assertCompatible(existing, entry);
  if (existing) return raw;
  const updated =
    agent.format === 'toml'
      ? `${raw}\n${stringify({ [agent.serverKey]: { [name]: entry } })}`
      : applyEdits(
          raw,
          modify(raw, [agent.serverKey, name], entry, {
            formattingOptions: { insertSpaces: true, tabSize: 2 },
          }),
        );
  parseConfig(updated, agent);
  return updated;
}

export const writeBridgeConfig = (agent: AgentConfig, input: BridgeConfiguration): Promise<void> =>
  writeAgentConfig(agent, (raw) => updatedBridgeConfig(raw, agent, input));
