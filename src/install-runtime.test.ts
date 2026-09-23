import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { expect, it } from 'vitest';
import { installRuntime } from './install-runtime.js';

it('keeps an installed CLI and its credential-store dependency after the original package disappears', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-runtime-'));
  try {
    const source = join(directory, 'source');
    const dependency = join(source, 'node_modules/@napi-rs/keyring');
    await mkdir(dependency, { recursive: true });
    await writeFile(
      join(dependency, 'package.json'),
      JSON.stringify({ name: '@napi-rs/keyring', main: 'index.js' }),
    );
    await writeFile(join(dependency, 'index.js'), 'exports.AsyncEntry = class {};');
    const program = join(source, 'cli.js');
    await writeFile(
      program,
      'import { AsyncEntry } from "@napi-rs/keyring"; process.stdout.write(typeof AsyncEntry);',
    );
    const installed = await installRuntime(join(directory, 'config'), program);
    await rm(source, { recursive: true });
    expect((await promisify(execFile)(process.execPath, [installed])).stdout).toBe('function');
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
