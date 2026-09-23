import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it } from 'vitest';
import { agentsFor } from './agents.js';
import { writeBridgeConfig } from './bridge-config.js';
import { verifySavedBridge } from './verify-bridge.js';

it('verifies an authenticated empty catalog by launching the exact saved stdio command', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-bridge-process-'));
  try {
    const cliPath = join(directory, 'fixture.mjs');
    await writeFile(
      cliPath,
      `import { createInterface } from 'node:readline';
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
    await rm(directory, { recursive: true, force: true });
  }
});
