import { readFile } from 'node:fs/promises';
import { isAbsolute } from 'node:path';
import { z } from 'zod';
import type { AgentConfig } from './agents.js';
import { CliError } from './cli-options.js';

const path = z
  .string()
  .min(1)
  .refine(isAbsolute)
  .refine((value) => !/[\r\n\0]/u.test(value));
const descriptor = z
  .object({
    id: z.string().regex(/^custom(?:-[a-z0-9-]+)?$/u),
    name: z.string().min(1).max(80),
    configPath: path,
    format: z.enum(['jsonc', 'toml']),
    serverKey: z.string().regex(/^[a-zA-Z_][a-zA-Z0-9_]*$/u),
    bridge: z.enum(['arguments', 'command-array']),
    bridgeTransport: z
      .record(z.string(), z.union([z.string(), z.boolean(), z.array(z.string())]))
      .default({}),
    skillsDirectory: path,
    hookConfig: z
      .object({
        path,
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
      })
      .optional(),
  })
  .strict();

export async function readCustomHarness(file: string): Promise<AgentConfig> {
  const parsed = descriptor.safeParse(JSON.parse(await readFile(file, 'utf8')));
  if (!parsed.success)
    throw new CliError('Invalid custom harness descriptor. See docs/agent-setup.md.');
  const { hookConfig, ...agent } = parsed.data;
  return {
    ...agent,
    ...(hookConfig ? { hookConfig } : {}),
    urlKey: 'url',
    transport: {},
    authentication: '',
  };
}
