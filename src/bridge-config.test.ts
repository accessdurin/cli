import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { afterEach, expect, it } from 'vitest';
import { agentsFor } from './agents.js';
import { parseConfig } from './config.js';
import { bridgeServerName, writeBridgeConfig } from './bridge-config.js';

const temporary: string[] = [];
afterEach(async () => {
  await Promise.all(temporary.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});
const args = [
  '/opt/durin/cli.js',
  'mcp',
  'serve',
  '--profile',
  'a'.repeat(64),
  '--config-dir',
  '/tmp/durin-profile',
];
const CASES = [
  { key: 'claude', value: { type: 'stdio', command: '/opt/node', args } },
  { key: 'codex', value: { command: '/opt/node', args } },
  { key: 'cursor', value: { command: '/opt/node', args } },
  { key: 'windsurf', value: { command: '/opt/node', args } },
  { key: 'opencode', value: { type: 'local', enabled: true, command: ['/opt/node', ...args] } },
];

it.each(CASES)(
  'configures the $key stdio bridge with a pinned profile and stable executable',
  async ({ key, value }) => {
    const home = await mkdtemp(join(tmpdir(), 'durin-bridge-'));
    temporary.push(home);
    const agent = agentsFor(home)[key]!;
    const initial =
      agent.format === 'toml'
        ? '# keep comment\nmodel="keep"\n'
        : '{ // keep comment\n "theme": "keep" }';
    await mkdir(dirname(agent.configPath), { recursive: true });
    await writeFile(agent.configPath, initial);
    const input = {
      nodePath: '/opt/node',
      cliPath: '/opt/durin/cli.js',
      profileId: 'a'.repeat(64),
      configDirectory: '/tmp/durin-profile',
    };
    await writeBridgeConfig(agent, input);
    const raw = await readFile(agent.configPath, 'utf8');
    expect(raw).toContain('keep comment');
    expect(parseConfig(raw, agent)).toMatchObject(parseConfig(initial, agent));
    expect(parseConfig(raw, agent)[agent.serverKey]).toEqual({
      [bridgeServerName(input.profileId)]: value,
    });
    await writeBridgeConfig(agent, input);
    expect(await readFile(agent.configPath, 'utf8')).toBe(raw);
  },
);
