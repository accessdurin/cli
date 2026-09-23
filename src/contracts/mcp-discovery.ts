import { z } from 'zod';

export const MCP_CATALOG_LIMITS = {
  pages: 64,
  tools: 10_000,
  pageBytes: 1_048_576,
  totalBytes: 8_388_608,
} as const;
const jsonObject = z.record(z.string(), z.json());
const jsonSchema = z.union([z.boolean(), jsonObject]);

/** Untrusted upstream data, retained for review; annotations never authorize execution. */
export const mcpDiscoveredToolSchema = z
  .object({
    name: z.string().min(1).max(128),
    title: z.string().optional(),
    description: z.string().optional(),
    inputSchema: z.object({ type: z.literal('object') }).catchall(z.json()),
    outputSchema: jsonSchema.optional(),
    annotations: jsonObject.optional(),
    _meta: jsonObject.optional(),
  })
  .catchall(z.json());

export const mcpToolsPageSchema = z
  .object({
    tools: z.array(mcpDiscoveredToolSchema).max(MCP_CATALOG_LIMITS.tools),
    nextCursor: z.string().max(16_384).nullable().optional(),
  })
  .catchall(z.json());

export const mcpToolsListSchema = mcpToolsPageSchema
  .extend({ nextCursor: z.never().optional() })
  .refine(
    (value) => new Set(value.tools.map((tool) => tool.name)).size === value.tools.length,
    'Duplicate tool identity',
  )
  .refine(
    (value) =>
      new TextEncoder().encode(JSON.stringify(value)).byteLength <= MCP_CATALOG_LIMITS.totalBytes,
    'Catalog exceeds limit',
  );

export type McpDiscoveredTool = z.infer<typeof mcpDiscoveredToolSchema>;
export type McpDiscoveredCatalog = Readonly<{
  tools: readonly McpDiscoveredTool[];
  reviewed: false;
  [key: string]: unknown;
}>;
