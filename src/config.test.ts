import { mkdtemp, readFile, writeFile, mkdir, rm, stat, symlink, lstat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { agentsFor } from './agents.js';
import { endpointFrom, parseConfig, updatedConfig, writeConfig } from './config.js';
import { validateEndpoint } from './endpoint.js';

const ENDPOINT = `https://mcp.getdurin.com/mcp/u/${'a'.repeat(64)}`;
const temporary: string[] = [];
const homeDirectory = async () => {
  const home = await mkdtemp(join(tmpdir(), 'durin-cli-test-'));
  temporary.push(home);
  return home;
};
afterEach(async () => {
  await Promise.all(temporary.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});

const CASES = [
  {
    key: 'claude',
    path: '.claude.json',
    root: 'mcpServers',
    value: { type: 'http', url: ENDPOINT },
  },
  { key: 'codex', path: '.codex/config.toml', root: 'mcp_servers', value: { url: ENDPOINT } },
  { key: 'cursor', path: '.cursor/mcp.json', root: 'mcpServers', value: { url: ENDPOINT } },
  {
    key: 'windsurf',
    path: '.codeium/windsurf/mcp_config.json',
    root: 'mcpServers',
    value: { serverUrl: ENDPOINT },
  },
  {
    key: 'opencode',
    path: '.config/opencode/opencode.json',
    root: 'mcp',
    value: { type: 'remote', enabled: true, url: ENDPOINT },
  },
];

describe('agent configuration adapters', () => {
  it('uses existing OpenCode JSONC without creating a competing JSON file', async () => {
    const home = await homeDirectory();
    const path = join(home, '.config/opencode/opencode.jsonc');
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, '{ // keep comment\n "theme": "system" }');
    const agent = agentsFor(home).opencode!;
    expect(agent.configPath).toBe(path);
    await writeConfig(agent, ENDPOINT);
    expect(endpointFrom(await readFile(path, 'utf8'), agent)).toBe(ENDPOINT);
    await expect(stat(path.slice(0, -1))).rejects.toThrow();
  });

  it('preserves a config symlink and updates its target', async () => {
    const home = await homeDirectory();
    const agent = agentsFor(home).claude!;
    const target = join(home, 'managed.json');
    await writeFile(target, '{}');
    await symlink(target, agent.configPath);
    await writeConfig(agent, ENDPOINT);
    expect((await lstat(agent.configPath)).isSymbolicLink()).toBe(true);
    expect(endpointFrom(await readFile(target, 'utf8'), agent)).toBe(ENDPOINT);
  });

  it.each(['a=[1 #', 'mcp_servers = { other = { command = "keep" } }'])(
    'refuses unsupported or malformed TOML without changing it: %s',
    (raw) => {
      expect(() => updatedConfig(raw, agentsFor('/unused').codex!, ENDPOINT)).toThrow();
    },
  );
  it.each(CASES)(
    'writes the actual $key format, round-trips and preserves settings',
    async ({ key, path, root, value }) => {
      const home = await homeDirectory();
      const agent = agentsFor(home)[key]!;
      expect(agent.configPath).toBe(join(home, path));
      const initial =
        agent.format === 'toml'
          ? '# keep comment\nmodel = "keep"\n[mcp_servers.other]\ncommand = "keep"\n'
          : `{\n// keep comment\n"theme":"keep", "${root}": {"other": {"command":"keep"}}\n}`;
      await mkdir(dirname(agent.configPath), { recursive: true });
      await writeFile(agent.configPath, initial);
      const before = parseConfig(initial, agent);
      await writeConfig(agent, ENDPOINT);
      const saved = await readFile(agent.configPath, 'utf8');
      expect(saved).toContain('keep comment');
      expect(parseConfig(saved, agent)).toMatchObject(before);
      expect(parseConfig(saved, agent)[root]).toMatchObject({ durin: value });
      expect(endpointFrom(saved, agent)).toBe(ENDPOINT);
      expect((await stat(agent.configPath)).mode & 0o777).toBe(0o600);
      await writeConfig(agent, ENDPOINT);
      expect(await readFile(agent.configPath, 'utf8')).toBe(saved);
    },
  );

  it.each(CASES)('creates $key config in an empty home', async ({ key }) => {
    const agent = agentsFor(await homeDirectory())[key]!;
    await writeConfig(agent, ENDPOINT);
    expect(endpointFrom(await readFile(agent.configPath, 'utf8'), agent)).toBe(ENDPOINT);
  });

  it.each(['null', '[]', '{', '{"mcpServers":[]}', '{"mcpServers":null}'])(
    'leaves malformed config untouched: %s',
    async (raw) => {
      const agent = agentsFor(await homeDirectory()).claude!;
      await writeFile(agent.configPath, raw);
      await expect(writeConfig(agent, ENDPOINT)).rejects.toThrow();
      expect(await readFile(agent.configPath, 'utf8')).toBe(raw);
    },
  );

  it('preserves an existing Durin credential and refuses to replace its endpoint', () => {
    const agent = agentsFor('/unused').cursor!;
    const raw = JSON.stringify({
      mcpServers: { durin: { url: ENDPOINT, headers: { Authorization: 'test-only' } } },
    });
    expect(updatedConfig(raw, agent, ENDPOINT)).toBe(raw);
    expect(() => updatedConfig(raw, agent, ENDPOINT.replace('/u/a', '/u/b'))).toThrow(
      'different Durin configuration',
    );
  });

  it.each([
    { type: 'stdio', url: ENDPOINT },
    { type: 'http', url: ENDPOINT, disabled: true },
    { url: ENDPOINT },
    { type: 'http', url: ENDPOINT, command: 'node' },
    { type: 'http', url: 123 },
  ])('rejects unusable Claude configurations %#', (server) => {
    expect(() =>
      endpointFrom(JSON.stringify({ mcpServers: { durin: server } }), agentsFor('/unused').claude!),
    ).toThrow();
  });

  it('honors configured agent locations', () => {
    const agents = agentsFor('/unused', {
      CLAUDE_CONFIG_DIR: '/custom/claude',
      CODEX_HOME: '/custom/codex',
      XDG_CONFIG_HOME: '/custom/xdg',
      OPENCODE_CONFIG: '/custom/open.jsonc',
    });
    expect(agents).toMatchObject({
      claude: { configPath: '/custom/claude/.claude.json' },
      codex: { configPath: '/custom/codex/config.toml' },
      opencode: { configPath: '/custom/open.jsonc' },
    });
    expect(agentsFor('/unused', { XDG_CONFIG_HOME: '/custom/xdg' }).opencode?.configPath).toBe(
      '/custom/xdg/opencode/opencode.json',
    );
  });

  it('installs Claude into its custom directory without touching the default file', async () => {
    const home = await homeDirectory();
    const defaultPath = join(home, '.claude.json');
    await writeFile(defaultPath, '{"keep":"default"}');
    const directory = join(home, 'custom-claude');
    const agent = agentsFor(home, { CLAUDE_CONFIG_DIR: directory }).claude!;
    await writeConfig(agent, ENDPOINT);
    expect(endpointFrom(await readFile(join(directory, '.claude.json'), 'utf8'), agent)).toBe(
      ENDPOINT,
    );
    expect(await readFile(defaultPath, 'utf8')).toBe('{"keep":"default"}');
  });
});

describe('endpoint validation', () => {
  it.each([
    '',
    'https://',
    'https://example.com',
    'http://mcp.getdurin.com/mcp/u/' + 'a'.repeat(64),
    ENDPOINT + '?token=secret',
    ENDPOINT + '#fragment',
    ENDPOINT.replace('https://', 'https://name:secret@'),
    ENDPOINT + '\n',
    ENDPOINT.replace('/mcp/u/', '/sse/'),
  ])('rejects invalid input %#', (value) => {
    expect(validateEndpoint(value)).toBeTypeOf('string');
  });
  it('accepts the issued personal endpoint format', () => {
    expect(validateEndpoint(ENDPOINT)).toBeUndefined();
  });
});
