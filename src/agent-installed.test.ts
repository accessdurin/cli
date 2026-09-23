import { mkdtemp, rm, writeFile, chmod } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it } from 'vitest';
import { agentInstalled } from './agent-installed.js';

it('detects an executable on PATH and rejects a non-executable placeholder', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-agent-path-'));
  try {
    expect(agentInstalled('claude', { PATH: directory }, 'linux')).toBe(false);
    await writeFile(join(directory, 'claude'), '#!/bin/sh\nexit 0\n', { mode: 0o600 });
    expect(agentInstalled('claude', { PATH: directory }, 'linux')).toBe(false);
    await chmod(join(directory, 'claude'), 0o700);
    expect(agentInstalled('claude', { PATH: directory }, 'linux')).toBe(true);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
