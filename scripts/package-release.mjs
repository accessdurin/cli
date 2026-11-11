#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { cp, mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { createRequire } from 'node:module';
import { copyRuntimePackage, packageDirectory } from '../src/runtime-packages.ts';

const require = createRequire(import.meta.url);
const manifest = require('../package.json');
const destination = resolve(process.env.DURIN_RELEASE_DIR ?? 'release');
const stage = resolve('.release-stage');
const run = (command, args, options = {}) =>
  execFileSync(command, args, { stdio: 'inherit', ...options });
if (!['linux', 'darwin', 'win32'].includes(process.platform))
  throw new Error('Unsupported release platform.');
if (!['x64', 'arm64'].includes(process.arch)) throw new Error('Unsupported release architecture.');
if (process.versions.node !== (await readFile('.node-version', 'utf8')).trim())
  throw new Error('Build release packages with the pinned Node 24 runtime.');
await rm(stage, { recursive: true, force: true });
await mkdir(stage, { recursive: true });
await mkdir(destination, { recursive: true });
const pack = runPack();
const packageFile = join(destination, `durin-cli-${manifest.version}.tgz`);
await cp(pack, packageFile);
await rm(pack);
// Install from pnpm's production lockfile; do not re-resolve transitive dependencies.
await writeFile(join(stage, 'package.json'), JSON.stringify({ ...manifest, scripts: {}, bin: {} }));
await cp('pnpm-lock.yaml', join(stage, 'pnpm-lock.yaml'));
await cp('pnpm-workspace.yaml', join(stage, 'pnpm-workspace.yaml'));
run(
  process.execPath,
  [
    join(dirname(require.resolve('pnpm')), require('pnpm').bin.pnpm),
    'install',
    '--prod',
    '--frozen-lockfile',
    '--ignore-scripts',
  ],
  { cwd: stage },
);
await materializeRuntimeModules();
await mkdir(join(stage, 'dist'), { recursive: true });
await cp('dist/cli.js', join(stage, 'dist', 'cli.js'));
await cp('dist/THIRD_PARTY_LICENSES.md', join(stage, 'dist', 'THIRD_PARTY_LICENSES.md'));
await cp('LICENSE', join(stage, 'LICENSE'));
await cp('README.md', join(stage, 'README.md'));
await mkdir(join(stage, 'runtime'), { recursive: true });
await cp(
  process.execPath,
  join(stage, 'runtime', process.platform === 'win32' ? 'node.exe' : 'node'),
);
// This exact-version upstream license includes Node's bundled third-party notices.
await cp(`licenses/node-v${process.versions.node}-LICENSE`, join(stage, 'runtime', 'LICENSE'));
await writeLaunchers();
run(join(stage, 'runtime', process.platform === 'win32' ? 'node.exe' : 'node'), [
  join(stage, 'dist', 'cli.js'),
  '--help',
]);
const target = `${process.platform === 'win32' ? 'windows' : process.platform}-${process.arch}`;
await createArchive(target);
if (process.platform === 'linux') await createDebian();
console.log(`Release packages created in ${destination}`);

function runPack() {
  const output = execFileSync('npm', ['pack', '--ignore-scripts', '--json'], {
    encoding: 'utf8',
    shell: process.platform === 'win32',
  });
  return JSON.parse(output)[0].filename;
}
async function writeLaunchers() {
  await mkdir(join(stage, 'bin'), { recursive: true });
  await writeFile(
    join(stage, 'bin', 'durin'),
    '#!/bin/sh\nDURIN_CLI_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"\nexec "$DURIN_CLI_ROOT/runtime/node" "$DURIN_CLI_ROOT/dist/cli.js" "$@"\n',
    { mode: 0o755 },
  );
  await writeFile(
    join(stage, 'durin.cmd'),
    '@echo off\r\n"%~dp0runtime\\node.exe" "%~dp0dist\\cli.js" %*\r\n',
  );
}
async function materializeRuntimeModules() {
  const targetModules = join(stage, 'runtime-modules');
  const from = join(stage, 'package.json');
  // Resolve through the locked pnpm install, then copy dependencies beside their
  // actual consumers. Dereferencing top-level pnpm links alone breaks resolution.
  await Promise.all(
    ['@napi-rs/keyring', '@sentry/node-cpu-profiler'].map((name) =>
      copyRuntimePackage(packageDirectory(name, from), join(targetModules, name)),
    ),
  );
  await rm(join(stage, 'node_modules'), { recursive: true, force: true });
  await rename(targetModules, join(stage, 'node_modules'));
}
async function createArchive(platformTarget) {
  const extension = process.platform === 'win32' ? 'zip' : 'tar.gz';
  const path = join(destination, `durin-${manifest.version}-${platformTarget}.${extension}`);
  if (process.platform === 'win32') run('tar.exe', ['-a', '-cf', path, '-C', stage, '.']);
  else run('tar', ['-czf', path, '-C', stage, '.']);
}
async function createDebian() {
  const debRoot = resolve('.deb-stage');
  await rm(debRoot, { recursive: true, force: true });
  const arch = process.arch === 'x64' ? 'amd64' : 'arm64';
  await cp(stage, join(debRoot, 'opt', 'durin'), { recursive: true, dereference: true });
  await mkdir(join(debRoot, 'DEBIAN'), { recursive: true });
  await writeFile(
    join(debRoot, 'DEBIAN', 'control'),
    `Package: durin-cli\nVersion: ${manifest.version}\nArchitecture: ${arch}\nMaintainer: Durin <support@getdurin.com>\nDepends: libc6 (>= 2.31), libstdc++6, libsecret-1-0, dbus-user-session\nSection: utils\nPriority: optional\nHomepage: https://github.com/accessdurin/cli\nDescription: Durin organization tools and coding-agent setup\n Includes Node 24 and the CLI runtime. Requires an unlocked credential store.\n`,
  );
  await mkdir(join(debRoot, 'usr', 'bin'), { recursive: true });
  await writeFile(
    join(debRoot, 'usr', 'bin', 'durin'),
    '#!/bin/sh\nexec /opt/durin/bin/durin "$@"\n',
    { mode: 0o755 },
  );
  run('dpkg-deb', [
    '--root-owner-group',
    '--build',
    debRoot,
    join(destination, `durin-cli_${manifest.version}_${arch}.deb`),
  ]);
  await rm(debRoot, { recursive: true, force: true });
}
