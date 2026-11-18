import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it, vi } from 'vitest';
import { agentsFor } from './agents.js';
import { writeBridgeConfig } from './bridge-config.js';
import { verifySavedBridge } from './verify-bridge.js';

it('verifies an authenticated empty catalog by launching the exact saved stdio command', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-bridge-process-'));
  try {
    vi.stubEnv('DO_NOT_TRACK', '1');
    // The bridge reads the OS credential store; on Linux that needs the session bus.
    vi.stubEnv('DBUS_SESSION_BUS_ADDRESS', 'unix:path=/run/user/1000/bus');
    vi.stubEnv('XDG_RUNTIME_DIR', '/run/user/1000');
    vi.stubEnv('DURIN_UNRELATED_SECRET', 'not inherited');
    const cliPath = join(directory, 'fixture.mjs');
    await writeFile(
      cliPath,
      `import { createInterface } from 'node:readline';
if (process.env.DO_NOT_TRACK !== '1' || process.env.NODE_ENV !== 'test') process.exit(1);
if (process.env.DBUS_SESSION_BUS_ADDRESS !== 'unix:path=/run/user/1000/bus' || process.env.XDG_RUNTIME_DIR !== '/run/user/1000') process.exit(1);
if (process.env.DURIN_UNRELATED_SECRET !== undefined) process.exit(1);
const replies = { initialize: { protocolVersion: '2025-11-25', serverInfo: { name: 'durin', version: '1' }, capabilities: { tools: {} } }, 'tools/list': { tools: [] } };
createInterface({ input: process.stdin }).on('line', (line) => { const request = JSON.parse(line); const result = replies[request.method]; if (result) process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: request.id, result }) + '\\n'); });`,
    );
    const agent = agentsFor(directory).claude!;
    const config = {
      cliPath,
      nodePath: process.execPath,
      configDirectory: directory,
      profileId: 'a'.repeat(64),
    };
    await writeBridgeConfig(agent, config);
    expect(await verifySavedBridge(agent, config)).toBe(0);
    await expect(
      verifySavedBridge(agent, { ...config, profileId: 'b'.repeat(64) }),
    ).rejects.toThrow('saved');
  } finally {
    vi.unstubAllEnvs();
    await rm(directory, { recursive: true, force: true });
  }
});
