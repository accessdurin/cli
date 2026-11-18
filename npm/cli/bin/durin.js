#!/usr/bin/env node
// Launcher for `npx @accessdurin/cli` and `npm install --global @accessdurin/cli`.
// The CLI is a native executable shipped in a per-platform package, which npm installs
// through optionalDependencies; this file only finds and runs it.
'use strict';
const { spawn } = require('node:child_process');
const path = require('node:path');

const PLATFORMS = {
  'darwin-arm64': '@accessdurin/cli-darwin-arm64',
  'darwin-x64': '@accessdurin/cli-darwin-x64',
  'linux-arm64': '@accessdurin/cli-linux-arm64',
  'linux-x64': '@accessdurin/cli-linux-x64',
  'win32-x64': '@accessdurin/cli-win32-x64',
};

function executable() {
  const name = PLATFORMS[`${process.platform}-${process.arch}`];
  if (!name) {
    process.stderr.write(
      `durin is not published for ${process.platform} ${process.arch}. See https://github.com/accessdurin/cli#install.\n`,
    );
    process.exit(1);
  }
  try {
    const manifest = require.resolve(`${name}/package.json`);
    return path.join(path.dirname(manifest), 'bin', process.platform === 'win32' ? 'durin.exe' : 'durin');
  } catch {
    process.stderr.write(
      `The durin executable package ${name} is missing. Reinstall @accessdurin/cli without --no-optional or --omit=optional.\n`,
    );
    process.exit(1);
  }
}

const child = spawn(executable(), process.argv.slice(2), { stdio: 'inherit', windowsHide: false });
// A terminal's Ctrl-C reaches the whole process group, so the CLI already gets SIGINT; the
// launcher only stays alive to report its exit. Signals sent to the launcher alone (an agent
// stopping its MCP server) are forwarded.
process.on('SIGINT', () => {});
for (const signal of ['SIGTERM', 'SIGHUP']) {
  process.on(signal, () => {
    if (child.exitCode === null) child.kill(signal);
  });
}
child.on('error', () => {
  process.stderr.write('The durin executable could not start. Reinstall @accessdurin/cli.\n');
  process.exit(1);
});
child.on('exit', (code, signal) => {
  if (signal) {
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
