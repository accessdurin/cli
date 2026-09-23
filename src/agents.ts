import { agentInstalled } from './agent-installed.js';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { existsSync } from 'node:fs';

export interface AgentConfig {
  readonly name: string;
  readonly configPath: string;
  readonly format: 'jsonc' | 'toml';
  readonly serverKey: string;
  readonly urlKey: string;
  readonly transport: Readonly<Record<string, string | boolean>>;
  readonly bridge: 'arguments' | 'command-array';
  readonly bridgeTransport: Readonly<Record<string, string | boolean>>;
  readonly authentication: string;
}

type AgentDefinition = Omit<AgentConfig, 'configPath'> & {
  readonly path: (home: string, env: NodeJS.ProcessEnv) => string;
};
const openCodePath = (home: string, env: NodeJS.ProcessEnv): string => {
  if (env.OPENCODE_CONFIG) return env.OPENCODE_CONFIG;
  const base = join(env.XDG_CONFIG_HOME ?? join(home, '.config'), 'opencode', 'opencode.json');
  return existsSync(`${base}c`) ? `${base}c` : base;
};
const AGENTS: Readonly<Record<string, AgentDefinition>> = {
  claude: {
    name: 'Claude Code',
    path: (home, env) => join(env.CLAUDE_CONFIG_DIR || home, '.claude.json'),
    format: 'jsonc',
    serverKey: 'mcpServers',
    urlKey: 'url',
    transport: { type: 'http' },
    bridge: 'arguments',
    bridgeTransport: { type: 'stdio' },
    authentication: 'Open Claude Code and use /mcp to authenticate Durin.',
  },
  codex: {
    name: 'Codex',
    path: (home, env) => join(env.CODEX_HOME ?? join(home, '.codex'), 'config.toml'),
    format: 'toml',
    serverKey: 'mcp_servers',
    urlKey: 'url',
    transport: {},
    bridge: 'arguments',
    bridgeTransport: {},
    authentication: 'Run `codex mcp login durin` to authenticate.',
  },
  cursor: {
    name: 'Cursor',
    path: (home) => join(home, '.cursor', 'mcp.json'),
    format: 'jsonc',
    serverKey: 'mcpServers',
    urlKey: 'url',
    transport: {},
    bridge: 'arguments',
    bridgeTransport: {},
    authentication: 'Open Cursor MCP settings and connect Durin to authenticate.',
  },
  windsurf: {
    name: 'Windsurf',
    path: (home) => join(home, '.codeium', 'windsurf', 'mcp_config.json'),
    format: 'jsonc',
    serverKey: 'mcpServers',
    urlKey: 'serverUrl',
    transport: {},
    bridge: 'arguments',
    bridgeTransport: {},
    authentication: 'Open Windsurf Cascade MCP settings and authenticate Durin.',
  },
  opencode: {
    name: 'OpenCode',
    path: openCodePath,
    format: 'jsonc',
    serverKey: 'mcp',
    urlKey: 'url',
    transport: { type: 'remote', enabled: true },
    bridge: 'command-array',
    bridgeTransport: { type: 'local', enabled: true },
    authentication: 'Run `opencode mcp auth durin` to authenticate.',
  },
};

export const agentsFor = (
  home: string,
  env: NodeJS.ProcessEnv = {},
): Readonly<Record<string, AgentConfig>> =>
  Object.fromEntries(
    Object.entries(AGENTS).map(([key, { path, ...agent }]) => [
      key,
      { ...agent, configPath: path(home, env) },
    ]),
  );

export const localAgents = () => agentsFor(homedir(), process.env);
export const agentOptions = () =>
  Object.entries(localAgents()).map(([value, config]) => ({
    value,
    label: config.name,
    hint: agentInstalled(value) ? 'installed' : 'not detected; install before continuing',
  }));
