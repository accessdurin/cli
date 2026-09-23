import { z } from 'zod';
import { safeCliUrl } from './cli-url.js';

export const cliProfileIdSchema = z.string().regex(/^[a-f0-9]{64}$/u);
export const cliProfileScopeSchema = z
  .object({
    appOrigin: z.url(),
    accountIssuer: z.url(),
    accountClientId: z.string().min(1),
    principalId: z.string().min(1),
    workosOrganizationId: z.string().min(1),
    organizationId: z.string().min(1),
    environment: z.enum(['sandbox', 'production']),
    resource: z.url(),
    mcpIssuer: z.url(),
    development: z.boolean(),
  })
  .strict()
  .refine(
    (scope) =>
      [scope.appOrigin, scope.accountIssuer, scope.resource, scope.mcpIssuer].every((url) =>
        safeCliUrl(url, scope.development),
      ),
    'Use HTTPS or explicitly enabled loopback development URLs.',
  )
  .refine(
    (scope) => URL.canParse(scope.appOrigin) && new URL(scope.appOrigin).origin === scope.appOrigin,
    'Use an application origin without a path.',
  )
  .refine(
    (scope) => /\/mcp\/u\/[a-f0-9]{64}$/u.test(scope.resource),
    'Use the resolved personal MCP resource.',
  );
export const cliProfileSchema = cliProfileScopeSchema
  .safeExtend({
    version: z.literal(1),
    id: cliProfileIdSchema,
  })
  .strict();
export type CliProfileScope = z.infer<typeof cliProfileScopeSchema>;
export type CliProfile = z.infer<typeof cliProfileSchema>;
