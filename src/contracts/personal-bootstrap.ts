import { z } from 'zod';
import { sessionSchema } from './account.js';
import { mcpEndpointSchema } from './mcp-endpoint.js';
import { countSchema, environmentSchema, roleSchema } from './primitives.js';

const packageSchema = z.enum(['standard', 'enterprise']);
export const organizationEntitlementsSchema = z
  .object({
    organizationId: z.string().min(1),
    package: packageSchema,
  })
  .strict();

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
  entitlements: organizationEntitlementsSchema,
  session: sessionSchema,
  mcp: mcpEndpointSchema,
  settings: z.object({
    retention: z.literal('metadata_only'),
    environment: environmentSchema,
  }),
});
export type PersonalWorkspace = z.infer<typeof personalWorkspaceSchema>;
export type PersonalBootstrap = z.infer<typeof personalBootstrapSchema>;
