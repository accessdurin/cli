import { parseArgs } from 'node:util';
import { homedir } from 'node:os';
import { resolve } from 'node:path';
import { safeCliUrl } from './contracts/cli-url.js';

export class CliError extends Error {}
export const HELP = `durin — Onboard and connect your organization to your preferred agent

Usage:
  durin                         Choose login or signup, onboard, and set up an agent
  durin login | signup          Start or resume interactive onboarding
  durin install                 Resume interactive agent onboarding
  durin verify                  Verify authenticated MCP access
  durin profiles list           Show saved organization profiles
  durin profiles use <id>       Change the terminal profile
  durin logout                 Revoke and clear this profile’s MCP grant
  durin tools list              List authorized tools as JSON
  durin tools inspect <name>    Print a tool's native schema
  durin tools call <name> --input <file|-> --idempotency-key <key>
                                Invoke a tool; retain the key when retrying
  durin mcp serve               Serve the selected profile over stdio
  durin mcp endpoint            Print the personal URL for a direct MCP client

Options:
  --profile <id>                Select an explicit organization profile
  --config-dir <path>           Profile directory (default: ~/.config/durin)
  --approval-id <id>            Resume a tool call after approval
  --app-url <origin>            Durin app origin (default: https://app.getdurin.com)
  --dev                        Allow loopback HTTP for local development
  -h, --help                   Show this help

Agent selection happens during onboarding. Direct MCP clients authorize separately.
`;
function parseCliArgs(args: readonly string[]) {
  const { values, positionals } = parseArgs({
    args: [...args],
    allowPositionals: true,
    options: {
      help: { type: 'boolean', short: 'h' },
      profile: { type: 'string' },
      'config-dir': { type: 'string' },
      input: { type: 'string' },
      'idempotency-key': { type: 'string' },
      'approval-id': { type: 'string' },
      'app-url': { type: 'string' },
      dev: { type: 'boolean' },
    },
  });
  return { values, positionals };
}
function resolvedOptions(values: ReturnType<typeof parseCliArgs>['values'], positionals: string[]) {
  const appOrigin = appUrl(values['app-url']);
  const development = values.dev ?? false;
  if (!safeCliUrl(appOrigin, development))
    throw new CliError('Use an HTTPS app origin, or --dev with loopback HTTP.');
  if (new URL(appOrigin).origin !== appOrigin)
    throw new CliError('--app-url must be an origin without a path.');
  return {
    values,
    positionals,
    appOrigin,
    development,
    directory: configDirectory(values['config-dir']),
  };
}
const configDirectory = (path: string | undefined) =>
  resolve(path ?? resolve(homedir(), '.config/durin'));
export function cliOptions(args: readonly string[]) {
  const { values, positionals } = parseCliArgs(args);
  return resolvedOptions(values, positionals);
}
export type CliOptions = ReturnType<typeof cliOptions>;

const appUrl = (value: string | undefined) => value ?? 'https://app.getdurin.com';
