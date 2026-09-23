import { z } from 'zod';

export const cliAuthConfigSchema = z
  .object({
    clientId: z.string().min(1),
    issuer: z.url(),
    providerOrigin: z.url(),
    appOrigin: z.url(),
  })
  .strict();

export const deviceChallengeSchema = z.object({
  device_code: z.string().min(1),
  user_code: z.string().min(1),
  verification_uri: z.url(),
  verification_uri_complete: z.url().optional(),
  expires_in: z.number().int().positive().max(1800),
  interval: z.number().int().positive().max(60).default(5),
});

export const deviceSessionSchema = z
  .object({
    access_token: z.string().min(1),
    refresh_token: z.string().min(1),
    organization_id: z.string().min(1).nullish(),
    user: z.object({ id: z.string().min(1) }),
  })
  .transform((value) => ({
    accessToken: value.access_token,
    refreshToken: value.refresh_token,
    organizationId: value.organization_id ?? null,
    principalId: value.user.id,
  }));

export const deviceErrorSchema = z.object({ error: z.string() });

export const authorizationCallbackSchema = z
  .object({
    state: z.string().min(1).max(256),
    code: z.string().min(1).max(4096).optional(),
    error: z.string().min(1).max(256).optional(),
    iss: z.url().optional(),
  })
  .refine((value) => Boolean(value.code) !== Boolean(value.error))
  .transform((value) =>
    value.code
      ? { kind: 'code' as const, code: value.code, state: value.state, issuer: value.iss }
      : { kind: 'denied' as const, state: value.state, issuer: value.iss },
  );
