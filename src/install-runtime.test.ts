import { chmod, mkdtemp, mkdir, readdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { afterEach, beforeEach, expect, it } from 'vitest';
import { installRuntime } from './install-runtime.js';

let directory = '';
let program = '';
beforeEach(async () => {
  directory = await mkdtemp(join(tmpdir(), 'durin-runtime-'));
  const source = join(directory, 'source');
  const dependency = join(source, 'node_modules/@napi-rs/keyring');
  await mkdir(dependency, { recursive: true });
  await writeFile(
    join(dependency, 'package.json'),
    JSON.stringify({ name: '@napi-rs/keyring', main: 'index.js' }),
  );
  await writeFile(join(dependency, 'index.js'), 'exports.AsyncEntry = class {};');
  program = join(source, 'cli.js');
  await writeFile(
    program,
    'import { AsyncEntry } from "@napi-rs/keyring"; process.stdout.write(typeof AsyncEntry);',
  );
});
afterEach(async () => {
  await rm(directory, { recursive: true, force: true });
});
const versionDirectory = async (config: string): Promise<string> => {
  const version = (await readdir(join(config, 'runtime'))).find((name) => name !== 'cli.mjs');
  return join(config, 'runtime', version!);
};

it('keeps an installed CLI and its credential-store dependency after the original package disappears', async () => {
  const installed = await installRuntime(join(directory, 'config'), program);
  await rm(join(directory, 'source'), { recursive: true });
  expect((await promisify(execFile)(process.execPath, [installed])).stdout).toBe('function');
});

it('reuses an identical installed runtime', async () => {
  const first = await installRuntime(join(directory, 'config'), program);
  await expect(installRuntime(join(directory, 'config'), program)).resolves.toBe(first);
});

it('installs a different credential-store build beside the existing runtime', async () => {
  const config = join(directory, 'config');
  await installRuntime(config, program);
  await writeFile(
    join(directory, 'source/node_modules/@napi-rs/keyring/index.js'),
    'exports.AsyncEntry = class Rebuilt {};',
  );
  const installed = await installRuntime(config, program);
  const versions = (await readdir(join(config, 'runtime'))).filter((name) => name !== 'cli.mjs');
  expect(versions).toHaveLength(2);
  expect((await promisify(execFile)(process.execPath, [installed])).stdout).toBe('function');
});

it.each([
  ['a modified file', 'cli.js'],
  ['an added file', 'node_modules/@napi-rs/keyring/planted.js'],
])('refuses an installed runtime with %s', async (_name, file) => {
  const config = join(directory, 'config');
  await installRuntime(config, program);
  await writeFile(join(await versionDirectory(config), file), 'planted');
  await expect(installRuntime(config, program)).rejects.toThrow('does not match this CLI');
});

it.skipIf(process.platform === 'win32')(
  'reuses a runtime staged from group- or world-writable package files',
  async () => {
    await chmod(join(directory, 'source/node_modules/@napi-rs/keyring/index.js'), 0o666);
    const first = await installRuntime(join(directory, 'config'), program);
    await expect(installRuntime(join(directory, 'config'), program)).resolves.toBe(first);
  },
);

it.skipIf(process.platform === 'win32')(
  'refuses to reuse a runtime that other users can modify',
  async () => {
    const config = join(directory, 'config');
    await installRuntime(config, program);
    await chmod(join(await versionDirectory(config), 'node_modules'), 0o777);
    await expect(installRuntime(config, program)).rejects.toThrow('does not match this CLI');
  },
);

it.skipIf(process.platform === 'win32')(
  'refuses a runtime directory that other users can access',
  async () => {
    const config = join(directory, 'config');
    await installRuntime(config, program);
    await chmod(join(config, 'runtime'), 0o777);
    await expect(installRuntime(config, program)).rejects.toThrow(
      `${join(config, 'runtime')} must be a directory owned by you`,
    );
  },
);

it.skipIf(process.platform === 'win32')(
  'refuses to install into a directory other users can write',
  async () => {
    const config = join(directory, 'config');
    await mkdir(config);
    await chmod(config, 0o777);
    await expect(installRuntime(config, program)).rejects.toThrow('chmod 700');
  },
);
