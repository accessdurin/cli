import { execFile } from 'node:child_process';
import { chmod, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { beforeAll, describe, expect, it } from 'vitest';
import { build } from 'vite';

const exec = promisify(execFile);
beforeAll(async () => {
  await build({ logLevel: 'silent' });
});

describe('CLI public interface', () => {
  it('requires a terminal for onboarding and documents all three access paths', async () => {
    await expect(exec(process.execPath, ['dist/cli.js'])).rejects.toMatchObject({
      code: 1,
      stderr: expect.stringContaining('terminal'),
    });
    const { stdout } = await exec(process.execPath, ['dist/cli.js', '--help']);
    expect(stdout).toContain('tools list');
    expect(stdout).toContain('mcp serve');
    expect(stdout).toContain('mcp endpoint');
  });
  it.each([
    ['tools', 'list', 'extra'],
    ['tools', 'list', '--input', 'unused.json'],
    ['login', '--profile', 'a'.repeat(64)],
    ['mcp', 'serve', '--dev'],
    ['tools', 'list', '--force'],
  ])('rejects unused arguments: %j', async (...args) => {
    await expect(exec(process.execPath, ['dist/cli.js', ...args])).rejects.toMatchObject({
      code: 1,
      stderr: expect.stringContaining('Invalid arguments'),
    });
  });
  it.skipIf(process.platform === 'win32')(
    'refuses a configuration directory other users can write',
    async () => {
      const directory = await mkdtemp(join(tmpdir(), 'durin-cli-'));
      try {
        await chmod(directory, 0o777);
        await expect(
          exec(process.execPath, ['dist/cli.js', 'profiles', 'list', '--config-dir', directory]),
        ).rejects.toMatchObject({ code: 1, stderr: expect.stringContaining('chmod 700') });
      } finally {
        await rm(directory, { recursive: true, force: true });
      }
    },
  );
  it('accepts --force for logout', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'durin-cli-'));
    try {
      const failure = await exec(process.execPath, [
        'dist/cli.js',
        'logout',
        '--force',
        '--config-dir',
        directory,
      ]).catch((error: { stderr: string }) => error);
      expect(failure).toMatchObject({
        stderr: expect.not.stringMatching(/Unknown option|Invalid arguments/u),
      });
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
  it.each(['--agent', '-a'])('rejects the removed %s option before onboarding', async (flag) => {
    await expect(
      exec(process.execPath, ['dist/cli.js', '--help', flag, 'claude']),
    ).rejects.toMatchObject({ code: 1, stderr: expect.stringContaining('Unknown option') });
  });
});
