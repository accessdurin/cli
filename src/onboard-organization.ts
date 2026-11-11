/* eslint-disable no-await-in-loop -- Each request depends on the preceding server response. */
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
type Flow = Dependencies & {
  readonly clock: PollingClock;
  readonly deadline: number;
  readonly organizationId: string | null;
};
const createFlow = (dependencies: Dependencies, organizationId: string | null): Flow => {
  const clock = dependencies.clock ?? pollingClock();
  return {
    ...dependencies,
    organizationId,
    clock,
    deadline: clock.now() + (dependencies.timeoutMs ?? 600_000),
  };
};
const assertFresh = (flow: Flow): void => {
  if (flow.clock.now() >= flow.deadline)
    throw new CliError('Organization setup is still pending. Run durin to resume.');
};
const checkedStatus = (flow: Flow, state: OnboardingStatus): OnboardingStatus => {
  if (state.organizationId !== flow.organizationId)
    throw new CliError('Organization changed during onboarding. Run durin to sign in again.');
  if (!state.allowed && state.journey !== 'organization_creator')
    throw new CliError(
      'Your organization is not ready. Contact your administrator, then run durin again.',
    );
  return state;
};
type StepHandler = (dependencies: Flow, state: OnboardingStatus) => Promise<OnboardingStatus>;
const question =
  (step: OnboardingAnswer['step']): StepHandler =>
  async (flow, state) => {
    if (!state.profile)
      throw new CliError('Organization onboarding is unavailable for this account.');
    const answer = onboardingAnswerSchema.parse(
      await flow.questions.answer(step, state.profile.version),
    );
    if (answer.step !== step)
      throw new CliError('Answer did not match the current onboarding question.');
    assertFresh(flow);
    return flow.api.answer(answer);
  };
const waitUntil = async (
  flow: Flow,
  initial: OnboardingStatus,
  ready: (value: OnboardingStatus) => boolean,
): Promise<OnboardingStatus> => {
  let state = checkedStatus(flow, initial);
  while (!ready(state)) {
    assertFresh(flow);
    await flow.clock.sleep(Math.min(2000, flow.deadline - flow.clock.now()));
    assertFresh(flow);
    state = checkedStatus(flow, await flow.api.status());
  }
  return state;
};
const complete: StepHandler = async (flow, state) => {
  const ready = await waitUntil(
    flow,
    state,
    (value) => value.allowed || value.tenantReady || value.step !== state.step,
  );
  if (ready.allowed || ready.step !== state.step) return ready;
  assertFresh(flow);
  return flow.api.complete();
};
const payment: StepHandler = async (flow, state) => {
  if (!state.organizationId) throw new CliError('Select an organization before payment setup.');
  await flow.questions.payment(state.organizationId);
  return waitUntil(flow, state, (value) => value.allowed || value.step !== 'payment');
};
const STEPS: Record<OnboardingStatus['step'], StepHandler> = {
  title: question('title'),
  headcount: question('headcount'),
  source: question('source'),
  payment,
  provisioning: complete,
  complete,
};
const advance = async (flow: Flow): Promise<OnboardingStatus> => {
  let state = checkedStatus(flow, await flow.api.status());
  while (!state.allowed) {
    assertFresh(flow);
    const step = state.step;
    const next = await STEPS[step](flow, state);
    // A mutation can return before its effects are visible. Poll instead of repeating it.
    state = await waitUntil(flow, next, (value) => value.allowed || value.step !== step);
  }
  return state;
};

export async function onboardOrganization(dependencies: Dependencies): Promise<OnboardingStatus> {
  const state = await dependencies.api.status();
  if (state.allowed) return state;
  const flow = createFlow(dependencies, state.organizationId);
  checkedStatus(flow, state);
  await dependencies.api.start();
  return advance(flow);
}
