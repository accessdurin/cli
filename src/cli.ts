#!/usr/bin/env node
import { validateCommandArguments } from './command-arguments.js';
import { cliOptions, CliError, HELP, type CliOptions } from './cli-options.js';
import { wizard } from './commands/wizard.js';
import { profileCommand, logoutCommand } from './profile-commands.js';
import { toolCommand } from './tool-commands.js';
import { mcpCommand, verifyProfile } from './mcp-command.js';

const COMMANDS: Readonly<Record<string, (options: CliOptions) => Promise<void>>> = {
  '': wizard,
  login: wizard,
  signup: wizard,
  install: wizard,
  tools: toolCommand,
  mcp: mcpCommand,
  verify: verifyProfile,
  profiles: profileCommand,
  logout: logoutCommand,
};
async function main(): Promise<void> {
  const options = cliOptions(process.argv.slice(2));
  if (options.values.help) {
    process.stdout.write(HELP);
    return;
  }
  validateCommandArguments(options);
  const command = options.positionals[0] ?? '';
  const handler = commandHandler(command);
  if (!handler) throw new CliError('Unknown command. Run durin --help.');
  await handler(options);
}
void Promise.resolve()
  .then(main)
  .catch((error: unknown) => {
    const message = error instanceof CliError ? error.message : safeError(error);
    process.stderr.write(`${message}\n`);
    process.exitCode = 1;
  });
const safeError = (error: unknown): string => {
  if (
    error instanceof TypeError &&
    'code' in error &&
    error.code === 'ERR_PARSE_ARGS_UNKNOWN_OPTION'
  )
    return 'Unknown option. Run durin --help.';
  return 'Durin could not complete this request. Run durin in a terminal to verify your profile and authorization; retain the same idempotency key when retrying a tool call.';
};

const commandHandler = (command: string) =>
  Object.hasOwn(COMMANDS, command) ? COMMANDS[command] : undefined;
