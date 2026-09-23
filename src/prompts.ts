import { isCancel, cancel } from '@clack/prompts';
import { CliError } from './cli-options.js';

export const guard = <T>(value: T): Exclude<T, symbol> => {
  if (isCancel(value)) {
    cancel('Cancelled.');
    throw new CliError('Setup cancelled before completion. Run durin to resume.');
  }
  return value as Exclude<T, symbol>;
};
