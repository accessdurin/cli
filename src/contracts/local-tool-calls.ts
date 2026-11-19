import { z } from 'zod';

/**
 * Mirrors Durin's local tool-call contract (accessdurin/durin
 * packages/shared/src/local-tool-calls.ts). The gateway validates the same rules.
 */
export const MCP_REPORT_LOCAL_TOOL_CALL_TOOL = 'durin_report_local_tool_call';
export const LOCAL_TOOL_CALL_ARGUMENT_BYTES = 16_384;

const label = (max: number) =>
  z
    .string()
    .trim()
    .min(1)
    .max(max)
    .regex(/^\P{Cc}+$/u);
export const reportIdentifierSchema = label(128).regex(/^[\p{L}\p{N}._:/-]+$/u);
export const mcpNameSchema = (max: number) => label(max).regex(/^[\p{L}\p{N} ._:/@+-]+$/u);
export const localToolCallSchema = z
  .object({
    reportId: z.uuid(),
    reporter: z.literal('cli'),
    agent: label(100),
    sessionId: reportIdentifierSchema.nullable(),
    turnId: reportIdentifierSchema.nullable(),
    toolCallId: reportIdentifierSchema.nullable(),
    mcpServer: mcpNameSchema(100),
    tool: mcpNameSchema(128),
    transport: z.enum(['stdio', 'loopback_http']),
    outcome: z.enum(['succeeded', 'failed', 'unknown']),
    argumentsJson: z.string().max(LOCAL_TOOL_CALL_ARGUMENT_BYTES),
    argumentsTruncated: z.boolean(),
    observedAt: z.iso.datetime(),
  })
  .strict();
export type LocalToolCall = z.infer<typeof localToolCallSchema>;
export const localToolCallResultSchema = z
  .object({ recordId: z.string(), duplicate: z.boolean() })
  .strict();
