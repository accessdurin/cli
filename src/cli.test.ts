import { execFile } from 'node:child_process';
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
  ])('rejects unused arguments: %j', async (...args) => {
    await expect(exec(process.execPath, ['dist/cli.js', ...args])).rejects.toMatchObject({
      code: 1,
      stderr: expect.stringContaining('Invalid arguments'),
    });
  });
  it.each(['--agent', '-a'])('rejects the removed %s option before onboarding', async (flag) => {
    await expect(
      exec(process.execPath, ['dist/cli.js', '--help', flag, 'claude']),
    ).rejects.toMatchObject({ code: 1, stderr: expect.stringContaining('Unknown option') });
  });
});
