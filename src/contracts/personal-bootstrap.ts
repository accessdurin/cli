import { z } from 'zod';
import { sessionSchema } from './account.js';
import { mcpEndpointSchema } from './mcp-endpoint.js';
import { countSchema, environmentSchema, roleSchema } from './primitives.js';

export const personalWorkspaceSchema = z
  .object({
    organization: z.object({ id: z.string(), name: z.string(), environment: environmentSchema }),
    membership: z.object({
      principalId: z.string(),
      role: roleSchema,
      active: z.boolean(),
      revocationEpoch: countSchema,
    }),
  })
  .strict();
export const personalBootstrapSchema = personalWorkspaceSchema.extend({
  session: sessionSchema,
  mcp: mcpEndpointSchema,
  settings: z.object({
    retention: z.literal('metadata_only'),
    environment: environmentSchema,
  }),
});
export type PersonalWorkspace = z.infer<typeof personalWorkspaceSchema>;
export type PersonalBootstrap = z.infer<typeof personalBootstrapSchema>;
