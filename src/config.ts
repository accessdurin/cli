import { CliError } from './cli-options.js';
import { readFile, mkdir, writeFile, rename, rm, realpath } from 'node:fs/promises';
import { dirname } from 'node:path';
import { randomUUID } from 'node:crypto';
import { applyEdits, modify, parse, type ParseError } from 'jsonc-parser';
import { parse as parseToml } from 'smol-toml';
import { z } from 'zod';
import type { AgentConfig } from './agents.js';
import { endpointSchema } from './endpoint.js';

const record = z.record(z.string(), z.unknown());
const parseJson = (raw: string): unknown => {
  const errors: ParseError[] = [];
  const value: unknown = parse(raw, errors, { allowTrailingComma: true });
  if (errors.length) throw new CliError('Invalid JSON configuration; fix it before installing.');
  return value;
};
const PARSERS = { jsonc: parseJson, toml: parseToml };
export const parseConfig = (raw: string, agent: AgentConfig) => {
  try {
    return record.parse(PARSERS[agent.format](raw));
  } catch {
    throw new CliError(
      'Invalid agent configuration. Fix its syntax before installing; no settings were changed.',
    );
  }
};

export const readConfig = async (path: string): Promise<string | null> => {
  try {
    return await readFile(path, 'utf8');
  } catch (error) {
    if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return null;
    throw new CliError('Cannot read agent configuration; check its permissions.', { cause: error });
  }
};

export const serverFrom = (config: Record<string, unknown>, agent: AgentConfig, name = 'durin') => {
  const servers = record.parse(
    config[agent.serverKey] === undefined ? {} : config[agent.serverKey],
  );
  return servers[name] === undefined ? null : record.parse(servers[name]);
};

export const endpointFrom = (raw: string, agent: AgentConfig): string | null => {
  const server = serverFrom(parseConfig(raw, agent), agent);
  if (!server) return null;
  if (server.enabled === false || server.disabled === true)
    throw new CliError('Durin is disabled.');
  validateTransport(server, agent);
  return endpointSchema.parse(server[agent.urlKey]);
};

const validateTransport = (server: Record<string, unknown>, agent: AgentConfig): void => {
  const expected = agent.transport.type;
  if (expected !== undefined && server.type !== expected)
    throw new CliError('Durin has an invalid transport type.');
  if (server.command !== undefined) throw new CliError('Durin must use the remote HTTP transport.');
};

const existingConfig = (raw: string, agent: AgentConfig, endpoint: string): string | null => {
  const server = serverFrom(parseConfig(raw, agent), agent);
  if (!server) return null;
  if (server[agent.urlKey] !== endpoint) {
    throw new CliError(
      'A different Durin configuration already exists. Review it in your agent settings before replacing it.',
    );
  }
  endpointFrom(raw, agent);
  return raw;
};

export const updatedConfig = (raw: string, agent: AgentConfig, endpoint: string): string => {
  endpointSchema.parse(endpoint);
  const existing = existingConfig(raw, agent, endpoint);
  if (existing !== null) return existing;
  const server = { ...agent.transport, [agent.urlKey]: endpoint };
  const updated =
    agent.format === 'toml'
      ? `${raw}\n[mcp_servers.durin]\nurl = ${JSON.stringify(endpoint)}\n`
      : applyEdits(
          raw,
          modify(raw, [agent.serverKey, 'durin'], server, {
            formattingOptions: { insertSpaces: true, tabSize: 2 },
          }),
        );
  endpointFrom(updated, agent);
  return updated;
};

const EMPTY_CONFIG = { toml: '', jsonc: '{}' };
const configFilePath = async (path: string): Promise<string> => {
  try {
    return await realpath(path);
  } catch (error) {
    if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return path;
    throw error;
  }
};
export const writeAgentConfig = async (
  agent: AgentConfig,
  update: (raw: string) => string,
): Promise<void> => {
  const path = await configFilePath(agent.configPath);
  const raw = await readConfig(path);
  const initial = raw ?? EMPTY_CONFIG[agent.format];
  const updated = update(initial);
  if (updated === raw) return;
  await mkdir(dirname(path), { recursive: true });
  const temporary = `${path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, updated, { mode: 0o600, flag: 'wx' });
    if ((await readConfig(path)) !== raw)
      throw new CliError('Configuration changed; retry installation.');
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
};

export const writeConfig = (agent: AgentConfig, endpoint: string): Promise<void> =>
  writeAgentConfig(agent, (raw) => updatedConfig(raw, agent, endpoint));
