import * as p from '@clack/prompts';
import { fileURLToPath } from 'node:url';
import type { CliOptions } from '../cli-options.js';
import { CliError } from '../cli-options.js';
import { runSetup } from '../setup.js';
import { terminalUi } from '../terminal-ui.js';

export async function wizard(options: CliOptions): Promise<void> {
  if (![process.stdin.isTTY, process.stdout.isTTY].every(Boolean))
    throw new CliError('Run durin in an interactive terminal to complete onboarding.');
  p.intro('Welcome to Durin');
  const intent = accountIntent(options.positionals[0]);
  await runSetup({
    appOrigin: options.appOrigin,
    development: options.development,
    directory: options.directory,
    programPath: fileURLToPath(import.meta.url),
    ui: terminalUi(),
    ...(intent ? { intent } : {}),
  });
}

const accountIntent = (command: string | undefined): 'signup' | 'login' | undefined =>
  command === 'signup' || command === 'login' ? command : undefined;
