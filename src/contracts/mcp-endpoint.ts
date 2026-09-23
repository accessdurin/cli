import { z } from 'zod';

export const mcpEndpointSchema = z.discriminatedUnion('status', [
  z.object({ status: z.literal('ready'), url: z.url() }).strict(),
  z.object({ status: z.literal('needs_setup'), url: z.url() }).strict(),
  z.object({ status: z.literal('unconfigured'), url: z.null() }).strict(),
]);
export type McpEndpoint = Readonly<z.infer<typeof mcpEndpointSchema>>;
