import { z } from 'zod';

const metadata = {
  annotations: z.record(z.string(), z.json()).optional(),
  _meta: z.record(z.string(), z.json()).optional(),
};
const uri = z.string().max(2048);
const mimeType = z.string().min(1).max(128);
const base64 = z
  .string()
  .max(65_536)
  .regex(/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/);
const resource = { uri, mimeType: mimeType.optional(), _meta: metadata._meta };
const content = z.union([
  z.object({ type: z.literal('text'), text: z.string().max(16_384), ...metadata }),
  z.object({ type: z.literal('image'), data: base64, mimeType, ...metadata }),
  z.object({ type: z.literal('audio'), data: base64, mimeType, ...metadata }),
  z.object({
    type: z.literal('resource_link'),
    uri,
    name: z.string(),
    title: z.string().optional(),
    description: z.string().optional(),
    mimeType: mimeType.optional(),
    size: z.number().nonnegative().optional(),
    icons: z.array(z.record(z.string(), z.json())).optional(),
    ...metadata,
  }),
  z.object({
    type: z.literal('resource'),
    resource: z.union([
      z.object({ ...resource, text: z.string().max(16_384) }).strict(),
      z.object({ ...resource, blob: base64 }).strict(),
    ]),
    ...metadata,
  }),
]);

/** Protocol content stays inert; the broker separately gates uninspectable content. */
export const mcpToolResultSchema = z
  .object({
    content: z.array(content).max(128),
    structuredContent: z.json().optional(),
    isError: z.boolean().optional(),
    _meta: metadata._meta,
  })
  .refine(
    (value) => new TextEncoder().encode(JSON.stringify(value)).byteLength <= 65_536,
    'Result exceeds limit',
  );
export type McpToolResult = z.infer<typeof mcpToolResultSchema>;

export const isTextMcpResult = (result: McpToolResult): boolean =>
  result.content.every(
    (item) => item.type === 'text' || (item.type === 'resource' && 'text' in item.resource),
  );
