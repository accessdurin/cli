#!/usr/bin/env node
import {
  flushTelemetry,
  initializeTelemetry,
  reportUnexpectedError,
  traceCommand,
} from './telemetry.js';
import { validateCommandArguments } from './command-arguments.js';
import { cliOptions, CliError, HELP, type CliOptions } from './cli-options.js';
import { wizard } from './commands/wizard.js';
import { profileCommand, logoutCommand } from './profile-commands.js';
import { toolCommand } from './tool-commands.js';
import { mcpCommand, verifyProfile } from './mcp-command.js';
import { assertPrivateDirectory } from './private-directory.js';
import { sanitizeConsole, terminalText } from './terminal-text.js';
import { hookCommand } from './install-hooks.js';
import { readHookInstallation, runHook } from './hook-runner.js';

const localHook = process.argv[2] === 'hooks' && process.argv[3] === 'run';

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
  hooks: async (options) => {
    if (options.positionals[1] !== 'run') return hookCommand(options);
    const installation = await readHookInstallation(
      options.directory,
      options.values.harness ?? '',
    );
    await runHook(installation, process.stdin);
    if (installation.format === 'cursor') process.stdout.write('{"permission":"allow"}\n');
  },
};
sanitizeConsole(console);
if (!localHook) initializeTelemetry();
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
  await assertPrivateDirectory(options.directory);
  await handler(options);
}
void Promise.resolve()
  .then(() => traceCommand(main))
  .catch((error: unknown) => {
    const message = error instanceof CliError ? error.message : unexpectedMessage(error);
    process.stderr.write(`${terminalText(message)}\n`);
    process.exitCode = localHook ? 2 : 1;
    if (!localHook) reportUnexpectedError(error);
  })
  .finally(flushTelemetry);
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

const unexpectedMessage = (error: unknown): string =>
  localHook ? 'Durin could not complete this local hook check.' : safeError(error);
