import { z } from 'zod';
import { mcpToolsListSchema } from './mcp-discovery.js';

const identifier = z
  .string()
  .min(1)
  .max(128)
  .regex(/^[a-zA-Z0-9_:./-]+$/);

export const cliToolCallSchema = z
  .object({
    name: z.string().min(1).max(128),
    arguments: z.record(z.string(), z.json()),
    idempotencyKey: identifier,
    approvalId: identifier.optional(),
  })
  .strict();
export type CliToolCall = z.infer<typeof cliToolCallSchema>;
export type CliToolCatalog = z.infer<typeof mcpToolsListSchema>;

export const cliMcpNegotiationSchema = z.object({
  id: z.union([z.string(), z.number()]),
  result: z.object({ protocolVersion: z.string() }),
});
