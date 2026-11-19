import { isIP } from 'node:net';
import { isAbsolute, join } from 'node:path';
import { z } from 'zod';
import type { AgentConfig } from './agents.js';
import { parseConfig, readConfig } from './config.js';
import type { ReportFormat } from './tool-call-event.js';

export type LocalTransport = 'stdio' | 'loopback_http';
type Source = Readonly<{ path: string; format: AgentConfig['format']; servers: string[] }>;
/** The harness's user-level MCP configuration that setup wrote the Durin bridge into. */
export type McpConfigLocation = Readonly<Pick<AgentConfig, 'configPath' | 'format' | 'serverKey'>>;
const record = z.record(z.string(), z.unknown());

/** Project-scoped MCP files each harness reads in addition to its user configuration. */
const PROJECT_SOURCES: Readonly<Record<ReportFormat, readonly Source[]>> = {
  claude: [{ path: '.mcp.json', format: 'jsonc', servers: ['mcpServers'] }],
  codex: [{ path: join('.codex', 'config.toml'), format: 'toml', servers: ['mcp_servers'] }],
  cursor: [{ path: join('.cursor', 'mcp.json'), format: 'jsonc', servers: ['mcpServers'] }],
  windsurf: [],
};
function sources(
  format: ReportFormat,
  agent: McpConfigLocation,
  cwd: string | undefined,
): Source[] {
  const user: Source = { path: agent.configPath, format: agent.format, servers: [agent.serverKey] };
  if (!cwd || !isAbsolute(cwd)) return [user];
  const projects = PROJECT_SOURCES[format].map(
    (source): Source => Object.assign({}, source, { path: join(cwd, source.path) }),
  );
  return [user, ...projects];
}
const nested = (value: unknown, path: readonly string[]): unknown =>
  path.reduce<unknown>((node, key) => record.safeParse(node).data?.[key], value);
/** Claude Code also keeps servers added with `--scope local` under projects[<cwd>]. */
const claudeProjectServers = (config: unknown, cwd: string | undefined) =>
  cwd ? nested(config, ['projects', cwd, 'mcpServers']) : undefined;
async function entryIn(source: Source, name: string, cwd: string | undefined) {
  const raw = await readConfig(source.path).catch(() => null);
  if (raw === null) return null;
  const config = parseConfig(raw, { format: source.format } as AgentConfig);
  const scopes = [...source.servers.map((key) => config[key]), claudeProjectServers(config, cwd)];
  return scopes.map((servers) => record.safeParse(nested(servers, [name])).data).find(Boolean);
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
const argv = (entry: Record<string, unknown>): string[] =>
  [entry.command, entry.args].flat().filter((part): part is string => typeof part === 'string');
/** The Durin bridge runs `<cli> mcp serve --profile …`; its calls already reach the gateway. */
const durinBridge = (entry: Record<string, unknown>): boolean =>
  ['mcp', 'serve', '--profile'].every((part) => argv(entry).includes(part));
const remoteUrl = (entry: Record<string, unknown>): string | undefined =>
  [entry.url, entry.serverUrl, entry.httpUrl].find((value) => typeof value === 'string') as
    | string
    | undefined;

const urlTransport = (url: string): LocalTransport | null =>
  loopbackUrl(url) ? 'loopback_http' : null;
const commandTransport = (entry: Record<string, unknown>): LocalTransport | null =>
  argv(entry).length > 0 ? 'stdio' : null;
const skipped = (entry: Record<string, unknown>): boolean =>
  [entry.disabled === true, entry.enabled === false, durinBridge(entry)].some(Boolean);

export function entryTransport(entry: Record<string, unknown>): LocalTransport | null {
  if (skipped(entry)) return null;
  const url = remoteUrl(entry);
  return url === undefined ? commandTransport(entry) : urlTransport(url);
}

/**
 * Finds the named server in the harness's MCP configuration and returns its
 * transport when it runs on this machine. Unknown, remote and Durin servers are
 * null, so they are never reported as local.
 */
export async function localServerTransport(input: {
  readonly format: ReportFormat;
  readonly agent: McpConfigLocation;
  readonly server: string;
  readonly cwd?: string | undefined;
}): Promise<LocalTransport | null> {
  const found = await sources(input.format, input.agent, input.cwd).reduce<
    Promise<Record<string, unknown> | null | undefined>
  >(
    async (previous, source) =>
      (await previous) ?? (await entryIn(source, input.server, input.cwd).catch(() => null)),
    Promise.resolve(null),
  );
  return found ? entryTransport(found) : null;
}
