#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

const RULES = [
  {
    name: 'private key',
    pattern: /-----BEGIN [A-Z ]*PRIVATE KEY-----\s+[A-Za-z0-9+/=\r\n]{64,}\s+-----END/,
  },
  { name: 'AWS access key', pattern: /\bAKIA[0-9A-Z]{16}\b/ },
  { name: 'GitHub token', pattern: /\bgh[pousr]_[A-Za-z0-9_]{36,}\b/ },
  { name: 'live Stripe key', pattern: /\b(?:sk|rk)_live_[A-Za-z0-9]{16,}\b/ },
  { name: 'Stripe webhook secret', pattern: /\bwhsec_[A-Za-z0-9]{16,}\b/ },
  { name: 'Slack token', pattern: /\bxox[baprs]-[0-9A-Za-z-]{20,}\b/ },
];
const ignored = new Set();
const skippedDirectories = new Set(['.git', 'node_modules', 'coverage', 'dist']);

const trackedFiles = () =>
  execFileSync('git', ['ls-files', '--cached', '--others', '--exclude-standard', '-z'], {
    encoding: 'utf8',
  })
    .split('\0')
    .filter(Boolean)
    .filter((file) => !ignored.has(file));

const workspaceFiles = (directory) =>
  readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    if (entry.isDirectory() && skippedDirectories.has(entry.name)) return [];
    const path = join(directory, entry.name);
    return entry.isDirectory() ? workspaceFiles(path) : [path];
  });

const filesToScan = () => {
  const files = trackedFiles();
  return files.length > 0 ? files : workspaceFiles(process.cwd());
};

const findingsFor = (file) => {
  const content = readFileSync(file, 'utf8');
  if (content.includes('\0')) return [];
  return RULES.filter(({ pattern }) => pattern.test(content)).map(({ name }) => ({ file, name }));
};

const files = filesToScan();
const findings = files.flatMap(findingsFor);
if (findings.length > 0) {
  console.error('Secret scan failed; review the named files without printing credential values:');
  findings.forEach(({ file, name }) => console.error(`  ${name}: ${file}`));
  process.exit(1);
}
console.log(`Secret scan passed for ${files.length} repository files.`);
