import { chmod, mkdir, mkdtemp, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { assertPrivateDirectory } from './private-directory.js';

let root = '';
beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), 'durin-private-'));
});
afterEach(async () => {
  vi.restoreAllMocks();
  await rm(root, { recursive: true, force: true });
});

it.skipIf(process.platform === 'win32')(
  'accepts a missing directory or one only its owner can write',
  async () => {
    await expect(assertPrivateDirectory(join(root, 'missing'))).resolves.toBeUndefined();
    await mkdir(join(root, 'config'), { mode: 0o700 });
    await expect(assertPrivateDirectory(join(root, 'config'))).resolves.toBeUndefined();
    await chmod(join(root, 'config'), 0o755);
    await expect(assertPrivateDirectory(join(root, 'config'))).resolves.toBeUndefined();
  },
);

it.skipIf(process.platform === 'win32').each([0o777, 0o770, 0o757])(
  'refuses a directory with mode %o',
  async (mode) => {
    await mkdir(join(root, 'config'));
    await chmod(join(root, 'config'), mode);
    await expect(assertPrivateDirectory(join(root, 'config'))).rejects.toThrow('chmod 700');
  },
);

it.skipIf(process.platform === 'win32')(
  "refuses another user's directory and a non-directory",
  async () => {
    await mkdir(join(root, 'config'), { mode: 0o700 });
    await writeFile(join(root, 'file'), '');
    await expect(assertPrivateDirectory(join(root, 'file'))).rejects.toThrow('owned by you');
    const { uid } = await stat(join(root, 'config'));
    vi.spyOn(process, 'getuid').mockReturnValue(uid + 1);
    await expect(assertPrivateDirectory(join(root, 'config'))).rejects.toThrow('owned by you');
  },
);
