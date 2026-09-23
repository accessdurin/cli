import { createReadStream } from 'node:fs';
import type { Readable } from 'node:stream';
import { cliToolCallSchema } from './contracts/cli-mcp.js';
import { type CliOptions, CliError } from './cli-options.js';
import { withProfileClient } from './profile-client.js';

export const printJson = (value: unknown): void => {
  process.stdout.write(`${JSON.stringify(value, null, 2)}\n`);
};
const inputArguments = async (path: string | undefined): Promise<unknown> => {
  if (!path) throw new CliError('Provide --input <JSON file|-> and --idempotency-key <key>.');
  const source = await readInput(path === '-' ? process.stdin : createReadStream(path));
  return JSON.parse(source);
};
async function readInput(input: Readable): Promise<string> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of input) {
    const bytes = Buffer.from(chunk);
    size += bytes.length;
    if (size > 1_048_576) throw new CliError('Tool input exceeds 1 MiB.');
    chunks.push(bytes);
  }
  return Buffer.concat(chunks).toString('utf8');
}
const inspect = async (options: CliOptions): Promise<void> => {
  const catalog = await withProfileClient(options, (client) => client.listTools());
  const tool = catalog.tools.find((entry) => entry.name === options.positionals[2]);
  if (!tool) throw new CliError('Tool is unavailable in this organization. Run durin tools list.');
  printJson(tool);
};
const call = async (options: CliOptions): Promise<void> => {
  const input = cliToolCallSchema.safeParse({
    name: options.positionals[2],
    arguments: await inputArguments(options.values.input),
    idempotencyKey: options.values['idempotency-key'],
    ...(options.values['approval-id'] ? { approvalId: options.values['approval-id'] } : {}),
  });
  if (!input.success)
    throw new CliError(
      'Invalid call. Supply a tool name, JSON object, and --idempotency-key; use --approval-id when required.',
    );
  const result = await withProfileClient(options, (client) => client.callTool(input.data));
  printJson(result);
  if (result.isError) process.exitCode = 1;
};
const ACTIONS: Readonly<Record<string, (options: CliOptions) => Promise<void>>> = {
  list: async (options) =>
    printJson(await withProfileClient(options, (client) => client.listTools())),
  inspect,
  call,
};
export async function toolCommand(options: CliOptions): Promise<void> {
  const action = options.positionals[1] ?? '';
  const handler = Object.hasOwn(ACTIONS, action) ? ACTIONS[action] : undefined;
  if (!handler) throw new CliError('Use durin tools list, inspect <name>, or call <name>.');
  await handler(options);
}
