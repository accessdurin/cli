import { createHash, randomUUID } from 'node:crypto';
import { createRequire } from 'node:module';
import { cp, mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
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
  const id = createHash('sha256').update(code).digest('hex');
  const runtime = join(directory, 'runtime');
  const version = join(runtime, id);
  const temporary = join(runtime, `.install-${randomUUID()}`);
  await mkdir(temporary, { recursive: true, mode: 0o700 });
  try {
    await assertPrivateDirectory(directory);
    await writeFile(join(temporary, 'package.json'), '{"type":"module"}\n', { mode: 0o600 });
    await writeFile(join(temporary, 'cli.js'), code, { mode: 0o600 });
    await copyCredentialStore(source, temporary);
    await rename(temporary, version).catch(async (error: unknown) => {
      ignoreExistingVersion(error);
      await assertSameRuntime(temporary, version);
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
/** An existing runtime is reused only when it is byte-for-byte the runtime just staged. */
async function assertSameRuntime(staged: string, installed: string): Promise<void> {
  if ((await treeDigest(staged)) !== (await treeDigest(installed)))
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
