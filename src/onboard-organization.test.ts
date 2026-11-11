import { expect, it, vi } from 'vitest';
import { onboardOrganization } from './onboard-organization.js';
import type { OnboardingStatus } from './contracts/onboarding.js';
import type { OrganizationOnboardingApi, OrganizationQuestions } from './ports/cli-onboarding.js';
import type { PollingClock } from './ports/cli-auth.js';

const status = (
  step: OnboardingStatus['step'],
  extra: Partial<OnboardingStatus> = {},
): OnboardingStatus => ({
  organizationId: 'org_one',
  journey: 'organization_creator',
  next: '/onboarding',
  allowed: false,
  step,
  tenantReady: false,
  profile: {
    organizationId: 'org_one',
    creatorId: 'user_one',
    enrollment: 'required',
    title: null,
    headcount: null,
    source: null,
    otherSource: null,
    paymentReady: false,
    completedAt: null,
    version: 0,
  },
  ...extra,
});
const fixture = (initial = status('title')) => {
  let current = initial,
    now = 0;
  const api = {
    status: vi.fn<OrganizationOnboardingApi['status']>(async () => current),
    start: vi.fn<OrganizationOnboardingApi['start']>(async () => {
      current = status('headcount');
    }),
    answer: vi.fn<OrganizationOnboardingApi['answer']>(async () => {
      current = status('payment');
      return current;
    }),
    complete: vi.fn<OrganizationOnboardingApi['complete']>(async () => {
      current = status('complete', { allowed: true });
      return current;
    }),
  };
  const questions = {
    answer: vi.fn<OrganizationQuestions['answer']>(async (_step, version) => ({
      step: 'headcount' as const,
      version,
      value: 10,
    })),
    payment: vi.fn<OrganizationQuestions['payment']>(async () => {
      current = status('provisioning');
    }),
  };
  const clock = {
    now: () => now,
    sleep: vi.fn<PollingClock['sleep']>(async (ms) => {
      now += ms;
    }),
  };
  return {
    api,
    questions,
    clock,
    timeoutMs: 10_000,
    set: (value: OnboardingStatus) => {
      current = value;
    },
  };
};

it('reloads progress after start and follows server steps through payment and provisioning', async () => {
  const f = fixture();
  f.clock.sleep.mockImplementation(async () => {
    f.set(status('complete', { tenantReady: true }));
  });
  expect((await onboardOrganization(f)).allowed).toBe(true);
  expect(f.questions.answer.mock.calls).toEqual([['headcount', 0]]);
  expect(f.api.answer.mock.calls).toEqual([[{ step: 'headcount', version: 0, value: 10 }]]);
  expect(f.questions.payment.mock.calls).toEqual([['org_one']]);
  expect(f.api.complete).toHaveBeenCalledTimes(1);
});

it.each(['question', 'completion'] as const)(
  'polls a stalled %s once submitted, then expires without repeating mutations',
  async (step) => {
    const f = fixture();
    f.api.start.mockImplementation(async () => {
      f.set(status(step === 'question' ? 'headcount' : 'complete', { tenantReady: true }));
    });
    f.api.answer.mockImplementation(async () => status('headcount'));
    f.api.complete.mockImplementation(async () => status('complete', { tenantReady: true }));
    await expect(onboardOrganization(f)).rejects.toThrow('still pending');
    expect(f.api.answer).toHaveBeenCalledTimes(step === 'question' ? 1 : 0);
    expect(f.api.complete).toHaveBeenCalledTimes(step === 'completion' ? 1 : 0);
    expect(f.clock.now()).toBe(10_000);
  },
);

it.each(['start', 'answer', 'poll', 'complete'] as const)(
  'rejects an organization change from %s, including a completed response',
  async (source) => {
    const f = fixture();
    const changed = status('complete', { organizationId: 'org_other', allowed: true });
    const change = {
      start: () =>
        f.api.start.mockImplementation(async () => {
          f.set(changed);
        }),
      answer: () => f.api.answer.mockResolvedValue(changed),
      poll: () =>
        f.questions.payment.mockImplementation(async () => {
          f.set(changed);
        }),
      complete: () => {
        f.api.start.mockImplementation(async () => {
          f.set(status('complete', { tenantReady: true }));
        });
        f.api.complete.mockResolvedValue(changed);
      },
    };
    change[source]();
    await expect(onboardOrganization(f)).rejects.toThrow('Organization changed');
  },
);

it('accepts already allowed accounts and refuses invited members without starting onboarding', async () => {
  const f = fixture(status('complete', { journey: 'existing', allowed: true }));
  expect((await onboardOrganization(f)).allowed).toBe(true);
  expect(f.api.start).not.toHaveBeenCalled();
  f.set(status('provisioning', { journey: 'invited_member' }));
  await expect(onboardOrganization(f)).rejects.toThrow('Contact your administrator');
  expect(f.api.start).not.toHaveBeenCalled();
});

it('returns to a server-requested question when provisioning changes step', async () => {
  const f = fixture();
  f.api.start.mockImplementation(async () => {
    f.set(status('provisioning'));
  });
  f.clock.sleep.mockImplementation(async () => {
    f.set(status('headcount'));
  });
  f.api.answer.mockResolvedValue(status('complete', { tenantReady: true }));
  expect((await onboardOrganization(f)).allowed).toBe(true);
  expect(f.questions.answer.mock.calls).toEqual([['headcount', 0]]);
  expect(f.api.complete).toHaveBeenCalledTimes(1);
  expect(f.questions.payment).not.toHaveBeenCalled();
});
