import { isIP } from 'node:net';
import { dirname, isAbsolute, join } from 'node:path';
import { z } from 'zod';
import type { AgentConfig } from './agents.js';
import { parseConfig, readConfig } from './config.js';
import type { McpTarget, ReportFormat } from './tool-call-event.js';

export type LocalTransport = 'stdio' | 'loopback_http';
/** The harness's user-level MCP configuration that setup wrote the Durin bridge into. */
export type McpConfigLocation = Readonly<Pick<AgentConfig, 'configPath' | 'format' | 'serverKey'>>;
/** One table of MCP servers: a file and the key path to the table inside it. */
type Scope = Readonly<{ path: string; format: AgentConfig['format']; table: readonly string[] }>;
type Entry = Record<string, unknown>;
export type LocalServer = Readonly<{ transport: LocalTransport; server: string; tool: string }>;
const record = z.record(z.string(), z.unknown());

/** Project files each harness reads, relative to a project folder. */
const PROJECT_FILES: Readonly<Record<ReportFormat, readonly Omit<Scope, never>[]>> = {
  claude: [{ path: '.mcp.json', format: 'jsonc', table: ['mcpServers'] }],
  codex: [{ path: join('.codex', 'config.toml'), format: 'toml', table: ['mcp_servers'] }],
  cursor: [{ path: join('.cursor', 'mcp.json'), format: 'jsonc', table: ['mcpServers'] }],
  windsurf: [],
};
/** The working directory and its parents, nearest first, so a `cd` into a subfolder still finds the project. */
export function ancestors(cwd: string | undefined): string[] {
  if (!cwd || !isAbsolute(cwd)) return [];
  const parent = dirname(cwd);
  return parent === cwd ? [cwd] : [cwd, ...ancestors(parent)].slice(0, 32);
}
const projectScopes = (format: ReportFormat, folders: readonly string[]): Scope[] =>
  folders.flatMap((folder) =>
    PROJECT_FILES[format].map(
      (file): Scope => Object.assign({}, file, { path: join(folder, file.path) }),
    ),
  );
/** Claude Code keeps `--scope local` servers in its user file under projects[<folder>]. */
const claudeLocalScopes = (agent: McpConfigLocation, folders: readonly string[]): Scope[] =>
  folders.map((folder) => ({
    path: agent.configPath,
    format: agent.format,
    table: ['projects', folder, 'mcpServers'],
  }));
/**
 * Scopes in the harness's precedence order: Claude Code local, then project, then user;
 * other harnesses project, then user.
 */
function scopes(format: ReportFormat, agent: McpConfigLocation, cwd: string | undefined): Scope[] {
  const folders = ancestors(cwd);
  const local = format === 'claude' ? claudeLocalScopes(agent, folders) : [];
  const user: Scope = { path: agent.configPath, format: agent.format, table: [agent.serverKey] };
  return [...local, ...projectScopes(format, folders), user];
}

const nested = (value: unknown, path: readonly string[]): unknown =>
  path.reduce<unknown>((node, key) => record.safeParse(node).data?.[key], value);
/** Claude Code replaces characters outside [A-Za-z0-9_-] in server names when it names tools. */
export const normalizedServer = (name: string) => name.replaceAll(/[^A-Za-z0-9_-]/gu, '_');
function matchingKey(table: Record<string, unknown>, server: string): string | undefined {
  if (Object.hasOwn(table, server)) return server;
  return Object.keys(table).find((key) => normalizedServer(key) === server);
}
type Reader = (scope: Scope) => Promise<Record<string, unknown> | null>;
async function load(scope: Scope) {
  const raw = await readConfig(scope.path).catch(() => null);
  return raw === null ? null : parseConfig(raw, { format: scope.format } as AgentConfig);
}
function configReader(): Reader {
  const files = new Map<string, Promise<Record<string, unknown> | null>>();
  return (scope) => {
    if (!files.has(scope.path))
      files.set(
        scope.path,
        load(scope).catch(() => null),
      );
    return files.get(scope.path) ?? Promise.resolve(null);
  };
}
type Match = Readonly<{ key: string; tool: string; entry: unknown }>;
async function found(scope: Scope, read: Reader, targets: readonly McpTarget[]) {
  const table = record.safeParse(nested(await read(scope), scope.table)).data ?? {};
  return targets
    .map((target) => ({ target, key: matchingKey(table, target.server) }))
    .map(({ target, key }): Match | null =>
      key ? { key, tool: target.tool, entry: table[key] } : null,
    )
    .find((match) => match !== null);
}

const LOOPBACK_NAMES = new Set(['localhost', '::1']);
const loopbackHost = (host: string): boolean =>
  [
    LOOPBACK_NAMES.has(host),
    host.endsWith('.localhost'),
    isIP(host) === 4 && host.startsWith('127.'),
  ].some(Boolean);
export function loopbackUrl(value: string): boolean {
  try {
    return loopbackHost(new URL(value).hostname.replace(/^\[|\]$/gu, '').toLowerCase());
  } catch {
    return false;
  }
}
const argv = (entry: Entry): string[] =>
  [entry.command, entry.args].flat().filter((part): part is string => typeof part === 'string');
const profileFlag = (part: string) => part === '--profile' || part.startsWith('--profile=');
/** The Durin bridge runs `<cli> mcp serve --profile …`; its calls already reach the gateway. */
const durinBridge = (entry: Entry): boolean => {
  const parts = argv(entry);
  return [parts.includes('mcp'), parts.includes('serve'), parts.some(profileFlag)].every(Boolean);
};
const remoteUrl = (entry: Entry): string | undefined =>
  [entry.url, entry.serverUrl, entry.httpUrl].find((value) => typeof value === 'string') as
    | string
    | undefined;
const urlTransport = (url: string): LocalTransport | null =>
  loopbackUrl(url) ? 'loopback_http' : null;
const commandTransport = (entry: Entry): LocalTransport | null =>
  argv(entry).length > 0 ? 'stdio' : null;
const skipped = (entry: Entry): boolean =>
  [entry.disabled === true, entry.enabled === false, durinBridge(entry)].some(Boolean);

export function entryTransport(value: unknown): LocalTransport | null {
  const entry = record.safeParse(value).data;
  if (!entry || skipped(entry)) return null;
  const url = remoteUrl(entry);
  return url === undefined ? commandTransport(entry) : urlTransport(url);
}

/**
 * Finds the server the call went to, in the harness's own precedence order, and
 * returns it when it runs on this machine. The first scope that defines the server
 * decides; remote, disabled, unknown and Durin servers are never local.
 */
export async function localServer(input: {
  readonly format: ReportFormat;
  readonly agent: McpConfigLocation;
  readonly targets: readonly McpTarget[];
  readonly cwd?: string | undefined;
}): Promise<LocalServer | null> {
  const read = configReader();
  const match = await scopes(input.format, input.agent, input.cwd).reduce<Promise<Match | null>>(
    async (previous, scope) =>
      (await previous) ?? (await found(scope, read, input.targets)) ?? null,
    Promise.resolve(null),
  );
  const transport = match ? entryTransport(match.entry) : null;
  return match && transport ? { transport, server: match.key, tool: match.tool } : null;
}
