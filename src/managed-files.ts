import { createHash, randomUUID } from 'node:crypto';
import { lstat, mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { CliError } from './cli-options.js';

export const textDigest = (value: string): string =>
  createHash('sha256').update(value).digest('hex');
const missing = (error: unknown): boolean =>
  error instanceof Error && 'code' in error && error.code === 'ENOENT';
export async function regularText(path: string): Promise<string | null> {
  try {
    if (!(await lstat(path)).isFile()) throw new CliError(`Expected a regular file: ${path}`);
    return await readFile(path, 'utf8');
  } catch (error) {
    if (missing(error)) return null;
    throw error;
  }
}

async function checkedParents(path: string): Promise<void> {
  const parent = dirname(path);
  if (parent === path) return;
  await checkedParents(parent);
  await checkParent(parent);
}
async function checkParent(parent: string): Promise<void> {
  try {
    if (!(await isTrustedDirectory(parent)))
      throw new CliError(`Integration directories must not be symlinks: ${parent}`);
  } catch (error) {
    if (!missing(error)) throw error;
  }
}

async function isTrustedDirectory(path: string): Promise<boolean> {
  const entry = await lstat(path);
  if (entry.isDirectory()) return true;
  // macOS /var and /tmp are system-owned links to /private.
  if (entry.isSymbolicLink() && entry.uid === 0) return (await stat(path)).isDirectory();
  return false;
}

export type FileChange = {
  readonly path: string;
  readonly before: string | null;
  readonly after: string;
};
export async function planManagedFile(
  path: string,
  after: string,
  previous?: string,
): Promise<FileChange> {
  await checkedParents(path);
  const before = await regularText(path);
  const compatible = before === null || before === after || textDigest(before) === previous;
  if (!compatible)
    throw new CliError(
      `Preserving an existing or edited file: ${path}. Move it aside before retrying setup.`,
    );
  return { path, before, after };
}

export async function saveFileChange(change: FileChange): Promise<void> {
  if (change.before === change.after) return;
  await checkedParents(change.path);
  await mkdir(dirname(change.path), { recursive: true, mode: 0o700 });
  const temporary = `${change.path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, change.after, { mode: 0o600, flag: 'wx' });
    if ((await regularText(change.path)) !== change.before)
      throw new CliError(`Configuration changed; retry installation: ${change.path}`);
    await backupChange(change);
    await rename(temporary, change.path);
  } finally {
    await rm(temporary, { force: true });
  }
}
async function backupChange(change: FileChange): Promise<void> {
  if (change.before === null) return;
  await writeFile(`${change.path}.durin-backup-${randomUUID()}`, change.before, {
    mode: 0o600,
    flag: 'wx',
  });
}
