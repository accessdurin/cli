import { CliError, type CliOptions } from './cli-options.js';

type Rule = { readonly positions: number; readonly flags: readonly string[] };
const profile = ['profile', 'config-dir'];
const onboarding = ['config-dir', 'app-url', 'dev'];
const RULES: Readonly<Record<string, Rule>> = {
  '': { positions: 0, flags: onboarding },
  login: { positions: 1, flags: onboarding },
  signup: { positions: 1, flags: onboarding },
  install: { positions: 1, flags: onboarding },
  verify: { positions: 1, flags: profile },
  logout: { positions: 1, flags: profile },
  'tools list': { positions: 2, flags: profile },
  'tools inspect': { positions: 3, flags: profile },
  'tools call': { positions: 3, flags: [...profile, 'input', 'idempotency-key', 'approval-id'] },
  'mcp serve': { positions: 2, flags: profile },
  'mcp endpoint': { positions: 2, flags: profile },
  'profiles list': { positions: 2, flags: ['config-dir'] },
  'profiles use': { positions: 3, flags: ['config-dir'] },
};
const commandKey = (positions: readonly string[]): string =>
  ['tools', 'mcp', 'profiles'].includes(positions[0] ?? '')
    ? positions.slice(0, 2).join(' ')
    : positions.slice(0, 1).join('');
export function validateCommandArguments(options: CliOptions): void {
  const key = commandKey(options.positionals);
  const rule = Object.hasOwn(RULES, key) ? RULES[key] : undefined;
  if (!rule) throw new CliError('Unknown command. Run durin --help.');
  const valid = [
    options.positionals.length === rule.positions,
    Object.keys(options.values).every((flag) => rule.flags.includes(flag)),
  ].every(Boolean);
  if (!valid) throw new CliError('Invalid arguments for this command. Run durin --help.');
}
