import { chmod, lchown, mkdir, mkdtemp, rm, stat, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { assertPrivateDirectory, replaceableByOthers } from './private-directory.js';

const posix = process.platform !== 'win32';
let root = '';
beforeEach(async () => {
  root = await mkdtemp(join(tmpdir(), 'durin-private-'));
});
afterEach(async () => {
  vi.restoreAllMocks();
  await rm(root, { recursive: true, force: true });
});

it.skipIf(!posix)('accepts a missing directory or one only its owner can access', async () => {
  await expect(assertPrivateDirectory(join(root, 'missing'))).resolves.toBeUndefined();
  await mkdir(join(root, 'config'), { mode: 0o700 });
  await expect(assertPrivateDirectory(join(root, 'config'))).resolves.toBeUndefined();
});

it.skipIf(!posix).each([0o777, 0o770, 0o757, 0o755, 0o750, 0o705])(
  'refuses a directory with mode %o',
  async (mode) => {
    await mkdir(join(root, 'config'));
    await chmod(join(root, 'config'), mode);
    await expect(assertPrivateDirectory(join(root, 'config'))).rejects.toThrow('chmod 700');
  },
);

it.skipIf(!posix)("refuses another user's directory and a non-directory", async () => {
  await mkdir(join(root, 'config'), { mode: 0o700 });
  await writeFile(join(root, 'file'), '');
  await expect(assertPrivateDirectory(join(root, 'file'))).rejects.toThrow('owned by you');
  const { uid } = await stat(join(root, 'config'));
  vi.spyOn(process, 'getuid').mockReturnValue(uid + 1);
  await expect(assertPrivateDirectory(join(root, 'config'))).rejects.toThrow('owned by you');
});

it.skipIf(!posix)(
  'refuses a path through a world-writable directory without the sticky bit',
  async () => {
    await mkdir(join(root, 'shared', 'config'), { recursive: true, mode: 0o700 });
    await chmod(join(root, 'shared'), 0o777);
    await expect(assertPrivateDirectory(join(root, 'shared', 'config'))).rejects.toThrow(
      `Another user can replace ${join(root, 'shared')}`,
    );
    await chmod(join(root, 'shared'), 0o1777);
    await expect(assertPrivateDirectory(join(root, 'shared', 'config'))).resolves.toBeUndefined();
  },
);

it('treats symlinks owned by another user as replaceable', () => {
  const link = (uid: number) => ({ uid, mode: 0o120777, isSymbolicLink: () => true });
  const directory = (mode: number) => ({ uid: 501, mode, isSymbolicLink: () => false });
  expect(replaceableByOthers(link(501), 501)).toBe(false);
  expect(replaceableByOthers(link(0), 501)).toBe(false);
  expect(replaceableByOthers(link(4242), 501)).toBe(true);
  expect(replaceableByOthers(directory(0o40777), 501)).toBe(true);
  expect(replaceableByOthers(directory(0o41777), 501)).toBe(false);
  expect(replaceableByOthers(directory(0o40775), 501)).toBe(false);
});

it.skipIf(!posix || process.getuid?.() !== 0)(
  'refuses a configured path through a symlink another user owns',
  async () => {
    await mkdir(join(root, 'real'), { mode: 0o700 });
    await symlink(join(root, 'real'), join(root, 'link'));
    await lchown(join(root, 'link'), 4242, 4242);
    await expect(assertPrivateDirectory(join(root, 'link'))).rejects.toThrow(
      `Another user can replace ${join(root, 'link')}`,
    );
  },
);
