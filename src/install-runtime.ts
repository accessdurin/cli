import { createHash, randomUUID } from 'node:crypto';
import { chmod, lstat, mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { join, relative } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import { assertPrivateDirectory } from './private-directory.js';
import { copyRuntimePackage, packageDirectory } from './runtime-packages.js';

export async function installRuntime(directory: string, source: string): Promise<string> {
  const code = await readFile(source);
  const runtime = join(directory, 'runtime');
  const temporary = join(runtime, `.install-${randomUUID()}`);
  await mkdir(temporary, { recursive: true, mode: 0o700 });
  try {
    await assertPrivateDirectory(directory);
    await assertPrivateDirectory(runtime);
    await writeFile(join(temporary, 'package.json'), '{"type":"module"}\n', { mode: 0o600 });
    await writeFile(join(temporary, 'cli.js'), code, { mode: 0o600 });
    await Promise.all(
      ['@napi-rs/keyring', '@sentry/node-cpu-profiler'].map((name) =>
        copyRuntimePackage(packageDirectory(name, source), join(temporary, 'node_modules', name)),
      ),
    );
    await restrictTree(temporary);
    const digest = await treeDigest(temporary);
    const id = createHash('sha256').update(digest).digest('hex');
    const version = join(runtime, id);
    await rename(temporary, version).catch(async (error: unknown) => {
      ignoreExistingVersion(error);
      await assertSameRuntime(digest, version);
    });
    await publishLauncher(runtime, id);
    return join(runtime, 'cli.mjs');
  } finally {
    await rm(temporary, { recursive: true, force: true });
  }
}
const ignoreExistingVersion = (error: unknown): void => {
  if (!z.object({ code: z.enum(['EEXIST', 'ENOTEMPTY']) }).safeParse(error).success) throw error;
};
const treePaths = async (root: string): Promise<string[]> => [
  root,
  ...(await readdir(root, { recursive: true, withFileTypes: true })).map((entry) =>
    join(entry.parentPath, entry.name),
  ),
];
/** Staged entries are never writable by group or others, so reuse can demand the same. */
const restrictTree = async (root: string): Promise<void> => {
  const paths = await treePaths(root);
  await Promise.all(paths.map(async (path) => chmod(path, (await lstat(path)).mode & 0o755)));
};
const othersCanModify = async (root: string): Promise<boolean> => {
  if (typeof process.getuid !== 'function') return false;
  const uid = process.getuid();
  const entries = await Promise.all((await treePaths(root)).map(async (path) => lstat(path)));
  return entries.some((info) => info.uid !== uid || (info.mode & 0o022) !== 0);
};
const treeDigest = async (root: string): Promise<string> => {
  const entries = await readdir(root, { recursive: true, withFileTypes: true });
  const files = await Promise.all(
    entries
      .filter((entry) => !entry.isDirectory())
      .map(async (entry) => {
        const path = join(entry.parentPath, entry.name);
        const content = entry.isFile()
          ? createHash('sha256')
              .update(await readFile(path))
              .digest('hex')
          : 'not-a-file';
        return `${relative(root, path)}\0${content}`;
      }),
  );
  return files.toSorted().join('\n');
};
/** Runtimes are named by their content; an existing one is reused only if it is still intact. */
async function assertSameRuntime(digest: string, installed: string): Promise<void> {
  if ((await othersCanModify(installed)) || (await treeDigest(installed)) !== digest)
    throw new CliError(
      `The installed Durin runtime at ${installed} does not match this CLI. Stop Durin bridges, remove that directory, then run durin again.`,
    );
}
async function publishLauncher(runtime: string, id: string): Promise<void> {
  const temporary = join(runtime, `launcher-${randomUUID()}.mjs`);
  try {
    await writeFile(temporary, `import ${JSON.stringify(`./${id}/cli.js`)};\n`, {
      flag: 'wx',
      mode: 0o600,
    });
    await rename(temporary, join(runtime, 'cli.mjs'));
  } finally {
    await rm(temporary, { force: true });
  }
}
