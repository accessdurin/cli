import { spawn } from 'node:child_process';
import type { Readable } from 'node:stream';
import { normalizeHook } from './hook-input.js';
import { validateChecker, checkerSchema } from './hook-checker.js';
import { z } from 'zod';
import { regularText } from './managed-files.js';
import { join } from 'node:path';
import { CliError } from './cli-options.js';

export const hookInstallationSchema = z
  .object({
    format: z.enum([
      'claude',
      'codex',
      'cursor',
      'windsurf',
      'opencode',
      'gemini',
      'copilot',
      'custom',
    ]),
    harness: z.string().regex(/^[a-z][a-z0-9-]{0,63}$/u),
    checker: checkerSchema,
    target: z.string(),
    installedAt: z.string(),
    handler: z.record(z.string(), z.unknown()).optional(),
    assetDigest: z.string().regex(/^[a-f0-9]{64}$/u),
  })
  .strict();
export type HookInstallation = z.infer<typeof hookInstallationSchema>;
export const hookInstallationPath = (directory: string, harness: string) => {
  if (!/^[a-z][a-z0-9-]{0,63}$/u.test(harness)) throw new CliError('Invalid harness identifier.');
  return join(directory, 'hooks', `${harness}.json`);
};
export async function readHookInstallation(
  directory: string,
  harness: string,
): Promise<HookInstallation> {
  const raw = await regularText(hookInstallationPath(directory, harness));
  if (raw === null)
    throw new CliError('No local checker configured. Run durin in an interactive terminal.');
  return hookInstallationSchema.parse(JSON.parse(raw));
}
async function readHookInput(input: Readable): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of input) {
    const bytes = Buffer.from(chunk);
    size += bytes.length;
    if (size > 1_048_576) throw new CliError('Local hook input exceeds 1 MiB.');
    chunks.push(bytes);
  }
  return JSON.parse(
    Buffer.concat(chunks)
      .toString('utf8')
      .replace(/^\uFEFF/u, ''),
  );
}

export async function runHook(installation: HookInstallation, input: Readable): Promise<void> {
  const event = normalizeHook(await readHookInput(input), installation.format);
  const checker = await validateChecker(installation.checker);
  await invokeChecker(checker, JSON.stringify({ ...event, durin_harness: installation.harness }));
}
async function invokeChecker(checker: HookInstallation['checker'], input: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(checker.command, checker.args, {
      shell: false,
      windowsHide: true,
      stdio: ['pipe', 'ignore', 'ignore'],
    });
    const timeout = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new CliError('The local checker timed out.'));
    }, checker.timeoutMs);
    child.once('error', () => {
      clearTimeout(timeout);
      reject(new CliError('The local checker could not start.'));
    });
    child.once('close', (code) => {
      clearTimeout(timeout);
      if (code === 0) resolve();
      else
        reject(
          new CliError(
            code === 2 ? 'The local checker denied this tool call.' : 'The local checker failed.',
          ),
        );
    });
    child.stdin.on('error', () => {});
    child.stdin.end(input);
  });
}
