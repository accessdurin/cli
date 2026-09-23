import { CliError } from './cli-options.js';
import type { OrganizationOnboardingApi, OrganizationQuestions } from './ports/cli-onboarding.js';
import {
  onboardingAnswerSchema,
  type OnboardingAnswer,
  type OnboardingStatus,
} from './contracts/onboarding.js';
import type { PollingClock } from './ports/cli-auth.js';
import { pollingClock } from './polling-clock.js';

type Dependencies = {
  readonly api: OrganizationOnboardingApi;
  readonly questions: OrganizationQuestions;
  readonly clock?: PollingClock;
  readonly timeoutMs?: number;
};
type Flow = Dependencies & { readonly clock: PollingClock; readonly deadline: number };
const createFlow = (dependencies: Dependencies): Flow => {
  const clock = dependencies.clock ?? pollingClock();
  return { ...dependencies, clock, deadline: clock.now() + (dependencies.timeoutMs ?? 600_000) };
};
type StepHandler = (dependencies: Flow, state: OnboardingStatus) => Promise<OnboardingStatus>;
const question =
  (step: OnboardingAnswer['step']): StepHandler =>
  async ({ api, questions }, state) => {
    if (!state.profile)
      throw new CliError('Organization onboarding is unavailable for this account.');
    const answer = onboardingAnswerSchema.parse(
      await questions.answer(step, state.profile.version),
    );
    if (answer.step !== step)
      throw new CliError('Answer did not match the current onboarding question.');
    return api.answer(answer);
  };
const waitUntil = async (
  flow: Flow,
  state: OnboardingStatus,
  ready: (value: OnboardingStatus) => boolean,
): Promise<OnboardingStatus> => {
  if (ready(state)) return state;
  if (flow.clock.now() >= flow.deadline)
    throw new CliError('Organization setup is still pending. Run durin to resume.');
  await flow.clock.sleep(Math.min(2000, flow.deadline - flow.clock.now()));
  const next = await flow.api.status();
  if (next.organizationId !== state.organizationId)
    throw new CliError('Organization changed during onboarding. Run durin to sign in again.');
  return waitUntil(flow, next, ready);
};
const complete: StepHandler = async (flow, state) => {
  const ready = await waitUntil(flow, state, (value) => value.tenantReady);
  return ready.allowed ? ready : flow.api.complete();
};
const payment: StepHandler = async (flow, state) => {
  if (!state.organizationId) throw new CliError('Select an organization before payment setup.');
  await flow.questions.payment(state.organizationId);
  return waitUntil(flow, state, (value) => value.step !== 'payment');
};
const STEPS: Record<OnboardingStatus['step'], StepHandler> = {
  title: question('title'),
  headcount: question('headcount'),
  source: question('source'),
  payment,
  provisioning: complete,
  complete,
};
const advance = async (dependencies: Flow, state: OnboardingStatus): Promise<OnboardingStatus> => {
  if (state.allowed) return state;
  const next = await STEPS[state.step](dependencies, state);
  return advance(dependencies, next);
};

export async function onboardOrganization(dependencies: Dependencies): Promise<OnboardingStatus> {
  const state = await dependencies.api.status();
  if (state.allowed) return state;
  if (state.journey !== 'organization_creator')
    throw new CliError(
      'Your organization is not ready. Contact your administrator, then run durin again.',
    );
  await dependencies.api.start();
  return advance(createFlow(dependencies), state);
}
