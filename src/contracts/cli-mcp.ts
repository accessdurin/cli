import { z } from 'zod';
import { mcpToolsListSchema } from './mcp-discovery.js';

export const cliIdentifierSchema = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-zA-Z0-9_:./-]+$/);

/** Agents interpolate tool names into shell commands, so only shell-inert names are accepted. */
export const cliToolNameSchema = z.string().regex(/^[A-Za-z0-9_][A-Za-z0-9_.:/-]{0,127}$/u);

export const cliToolCallSchema = z
  .object({
    name: cliToolNameSchema,
    arguments: z.record(z.string(), z.json()),
    idempotencyKey: cliIdentifierSchema,
    approvalId: cliIdentifierSchema.optional(),
  })
  .strict();
export type CliToolCall = z.infer<typeof cliToolCallSchema>;
export type CliToolCatalog = z.infer<typeof mcpToolsListSchema>;

export const cliMcpNegotiationSchema = z.object({
  id: z.union([z.string(), z.number()]),
  result: z.object({ protocolVersion: z.string() }),
});
