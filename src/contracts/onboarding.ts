import { z } from 'zod';

export const DISCOVERY_SOURCES = [
  { value: 'search', label: 'Search' },
  { value: 'recommendation', label: 'Recommendation' },
  { value: 'linkedin', label: 'LinkedIn' },
  { value: 'x', label: 'X' },
  { value: 'community', label: 'Community' },
  { value: 'article', label: 'Article' },
  { value: 'other', label: 'Other' },
] as const;
export const discoverySourceSchema = z.enum([
  'search',
  'recommendation',
  'linkedin',
  'x',
  'community',
  'article',
  'other',
]);
export const onboardingStepSchema = z.enum([
  'title',
  'headcount',
  'source',
  'payment',
  'provisioning',
  'complete',
]);
export const onboardingProfileSchema = z.object({
  organizationId: z.string(),
  creatorId: z.string(),
  enrollment: z.enum(['required', 'exempt']),
  title: z.string().trim().min(1).max(120).nullable(),
  headcount: z.number().int().min(1).max(100_000_000).nullable(),
  source: discoverySourceSchema.nullable(),
  otherSource: z.string().trim().min(1).max(240).nullable(),
  paymentReady: z.boolean(),
  completedAt: z.number().int().nullable(),
  version: z.number().int().nonnegative(),
});
export type OnboardingProfile = z.infer<typeof onboardingProfileSchema>;
const answerVersion = { version: z.number().int().nonnegative() };
export const onboardingAnswerSchema = z.discriminatedUnion('step', [
  z
    .object({
      ...answerVersion,
      step: z.literal('title'),
      value: z.string().trim().min(1).max(120),
    })
    .strict(),
  z
    .object({
      ...answerVersion,
      step: z.literal('headcount'),
      value: z.number().int().min(1).max(100_000_000),
    })
    .strict(),
  z
    .object({
      ...answerVersion,
      step: z.literal('source'),
      value: discoverySourceSchema,
      other: z.string().trim().min(1).max(240).optional(),
    })
    .strict()
    .refine((answer) => answer.value !== 'other' || Boolean(answer.other), {
      message: 'Tell us where you heard about Durin.',
      path: ['other'],
    }),
]);
export type OnboardingAnswer = z.infer<typeof onboardingAnswerSchema>;
export const onboardingAccessSchema = z.object({
  organizationId: z.string().nullable(),
  journey: z.enum(['organization_creator', 'invited_member', 'existing', 'unassigned']),
  next: z.enum(['/onboarding', '/connections']),
  allowed: z.boolean(),
});
export const onboardingStatusSchema = onboardingAccessSchema.extend({
  profile: onboardingProfileSchema.nullable(),
  step: onboardingStepSchema,
  tenantReady: z.boolean(),
});
export type OnboardingStatus = z.infer<typeof onboardingStatusSchema>;
