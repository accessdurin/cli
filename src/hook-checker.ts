import { access, constants, readFile, stat } from 'node:fs/promises';
import { homedir } from 'node:os';
import { isAbsolute, join } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';

const executable = z
  .string()
  .min(1)
  .refine(isAbsolute)
  .refine((value) => !/[\r\n\0]/u.test(value));
export const checkerSchema = z
  .object({
    command: executable,
    args: z
      .array(z.string().refine((value) => !value.includes('\0')))
      .max(64)
      .default([]),
    timeoutMs: z.number().int().min(50).max(30_000).default(5000),
  })
  .strict();
export type HookChecker = z.infer<typeof checkerSchema>;
export async function validateChecker(value: unknown): Promise<HookChecker> {
  const checker = checkerSchema.safeParse(value);
  if (!checker.success)
    throw new CliError(
      'Invalid checker. Use an absolute executable, args array, and timeoutMs (50–30000).',
    );
  try {
    await access(checker.data.command, constants.X_OK);
    if (!(await stat(checker.data.command)).isFile())
      throw new Error('Expected an executable file.');
  } catch {
    throw new CliError('The selected local checker is missing or is not executable.');
  }
  return checker.data;
}
export async function readChecker(path: string): Promise<HookChecker> {
  return validateChecker(JSON.parse(await readFile(path, 'utf8')));
}

export async function endpointChecker(agent: string): Promise<HookChecker | null> {
  if (!['claude', 'codex'].includes(agent)) return null;
  const candidates = endpointCommands(agent);
  const available = await Promise.all(
    candidates.map((candidate) => validateChecker(candidate).catch(() => null)),
  );
  return available.find((candidate) => candidate !== null) ?? null;
}
function endpointCommands(agent: string): HookChecker[] {
  if (process.platform !== 'darwin') return [];
  return [join(homedir(), 'Applications'), '/Applications'].map((root) => ({
    command: join(root, 'Durin Endpoint Client.app', 'Contents', 'Helpers', 'observer-preflight'),
    args: ['--client', agent],
    timeoutMs: 10_000,
  }));
}
