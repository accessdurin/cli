import { lstat, realpath, stat } from 'node:fs/promises';
import type { Stats } from 'node:fs';
import { dirname } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';

const missing = (error: unknown): null => {
  if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return null;
  throw error;
};
const withAncestors = (path: string): string[] => {
  const parent = dirname(path);
  return parent === path ? [path] : [path, ...withAncestors(parent)];
};
/** Another user can swap a symlink they own, or any entry of a world-writable non-sticky directory. */
export const replaceableByOthers = (
  info: Pick<Stats, 'uid' | 'mode' | 'isSymbolicLink'>,
  uid: number,
): boolean =>
  info.isSymbolicLink() ? ![uid, 0].includes(info.uid) : (info.mode & 0o1002) === 0o002;

const unsafeReason = (info: Stats): string =>
  info.isSymbolicLink()
    ? 'is a symlink owned by another user'
    : 'is world-writable without the sticky bit';
async function assertSafePath(directory: string, uid: number): Promise<void> {
  const paths = [...withAncestors(directory), ...withAncestors(await realpath(directory))];
  const entries = await Promise.all(paths.map(async (path) => ({ path, info: await lstat(path) })));
  const unsafe = entries.find(({ info }) => replaceableByOthers(info, uid));
  if (unsafe)
    throw new CliError(
      `${unsafe.path}, on the path to the Durin configuration directory, ${unsafeReason(unsafe.info)}, so another user could replace it. Choose another --config-dir.`,
    );
}
/** Agents execute the runtime stored here, so other local users must not control it. */
export async function assertPrivateDirectory(directory: string): Promise<void> {
  if (typeof process.getuid !== 'function') return;
  const uid = process.getuid();
  const info = await stat(directory).catch(missing);
  if (!info) return;
  if ([!info.isDirectory(), info.uid !== uid, (info.mode & 0o077) !== 0].some(Boolean))
    throw new CliError(
      `The Durin directory ${directory} must be a directory owned by you that only you can access. Run chmod 700 on it, or choose another --config-dir.`,
    );
  await assertSafePath(directory, uid);
}
