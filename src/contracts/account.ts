import { z } from 'zod';
import { roleSchema } from './primitives.js';
import { onboardingAccessSchema } from './onboarding.js';

export const sessionSchema = z.object({
  mode: z.enum(['dev', 'workos']),
  principalId: z.string(),
  name: z.string(),
  email: z.string(),
  emailVerified: z.boolean().optional(),
  role: roleSchema,
  organizationId: z.string(),
  organizations: z.array(z.object({ id: z.string(), name: z.string() })),
  onboarding: onboardingAccessSchema.optional(),
});

export const apiErrorSchema = z.object({
  error: z.object({ code: z.string(), message: z.string() }),
  requestId: z.string().optional(),
});

export const organizationCreatedSchema = z.object({
  organizationId: z.string(),
  workosOrganizationId: z.string(),
  status: z.enum(['provisioning_pending', 'ready']),
});

export const organizationCreateSchema = z
  .object({ name: z.string().min(2).max(80), idempotencyKey: z.uuid() })
  .strict();
