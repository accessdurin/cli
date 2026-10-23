import { createHash, randomUUID } from 'node:crypto';
import { createRequire } from 'node:module';
import {
  chmod,
  cp,
  lstat,
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from 'node:fs/promises';
import { dirname, join, relative } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import { assertPrivateDirectory } from './private-directory.js';

const manifestSchema = z.object({
  optionalDependencies: z.record(z.string(), z.string()).default({}),
});
const packageDirectory = (name: string, from: string): string =>
  dirname(createRequire(from).resolve(`${name}/package.json`));
const optionalDirectory = (name: string, from: string): string | null => {
  try {
    return packageDirectory(name, from);
  } catch (error) {
    if (z.object({ code: z.literal('MODULE_NOT_FOUND') }).safeParse(error).success) return null;
    throw error;
  }
};
async function copyCredentialStore(source: string, target: string): Promise<void> {
  const directory = packageDirectory('@napi-rs/keyring', source);
  await cp(directory, join(target, 'node_modules/@napi-rs/keyring'), {
    recursive: true,
    dereference: true,
  });
  const manifest = manifestSchema.parse(
    JSON.parse(await readFile(join(directory, 'package.json'), 'utf8')),
  );
  await Promise.all(
    Object.keys(manifest.optionalDependencies).map(async (name) => {
      const path = optionalDirectory(name, join(directory, 'index.js'));
      if (path)
        await cp(path, join(target, 'node_modules', name), { recursive: true, dereference: true });
    }),
  );
}

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
    await copyCredentialStore(source, temporary);
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
