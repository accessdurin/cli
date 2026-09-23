import * as p from '@clack/prompts';
import { randomUUID } from 'node:crypto';
import type { SetupUi } from './ports/cli-setup.js';
import {
  DISCOVERY_SOURCES,
  onboardingAnswerSchema,
  type OnboardingAnswer,
} from './contracts/onboarding.js';
import { organizationCreateSchema } from './contracts/account.js';
import { guard } from './prompts.js';
import { agentOptions } from './agents.js';
import { openBrowser } from './browser.js';

const openAuthorization = async (url: string): Promise<void> => {
  p.note(url, 'Continue in your browser; this terminal will wait');
  await openBrowser(url);
};
const textAnswer = async (
  step: 'title' | 'headcount',
  version: number,
): Promise<OnboardingAnswer> => {
  const values = {
    title: { message: 'What is your role or job title?', value: (value: string) => value.trim() },
    headcount: {
      message: 'How many people work at your organization?',
      value: (value: string) => Number(value),
    },
  };
  const question = values[step];
  const input = guard(
    await p.text({
      message: question.message,
      validate: (value) =>
        onboardingAnswerSchema.safeParse({ step, version, value: question.value(value ?? '') })
          .success
          ? undefined
          : 'Enter a valid answer.',
    }),
  );
  return onboardingAnswerSchema.parse({ step, version, value: question.value(input) });
};
const sourceAnswer = async (version: number): Promise<OnboardingAnswer> => {
  const value = guard(
    await p.select({ message: 'Where did you hear about Durin?', options: [...DISCOVERY_SOURCES] }),
  );
  const other =
    value === 'other'
      ? guard(
          await p.text({
            message: 'Where did you hear about us?',
            validate: (detail) =>
              detail?.trim() && detail.trim().length <= 240 ? undefined : 'Enter 1–240 characters.',
          }),
        )
      : undefined;
  return onboardingAnswerSchema.parse({
    step: 'source',
    version,
    value,
    ...(other ? { other } : {}),
  });
};
const answer = (step: OnboardingAnswer['step'], version: number): Promise<OnboardingAnswer> =>
  step === 'source' ? sourceAnswer(version) : textAnswer(step, version);
const organizationName = async (): Promise<string> =>
  guard(
    await p.text({
      message: 'What is your organization called?',
      validate: (value) =>
        organizationCreateSchema.safeParse({ name: value?.trim(), idempotencyKey: randomUUID() })
          .success
          ? undefined
          : 'Enter an organization name of 2–80 characters.',
    }),
  );

const account: SetupUi['account'] = {
  intent: async () =>
    guard(
      await p.select({
        message: 'Log in or create an account?',
        options: [
          { value: 'login' as const, label: 'Log in' },
          { value: 'signup' as const, label: 'Create an account' },
        ],
      }),
    ),
  authorize: async (prompt, intent) => {
    const action =
      intent === 'signup'
        ? 'Choose Create an account in the browser, then authorize this device.'
        : 'Sign in and authorize this device.';
    p.note(`${action}\nDevice code: ${prompt.code}`, 'Account authorization');
    await openAuthorization(prompt.url);
  },
};

export const terminalUi = (): SetupUi => ({
  account,
  organizations: {
    choose: async (organizations) =>
      guard(
        await p.select({
          message: 'Which organization should this agent use?',
          options: organizations.map((organization) => ({
            value: organization.id,
            label: organization.name,
          })),
        }),
      ),
    create: organizationName,
  },
  onboarding: {
    answer,
    payment: async (organizationId) => {
      p.log.info(
        `Complete payment setup for ${organizationId} in the browser. Waiting for verified payment and provisioning.`,
      );
    },
  },
  agent: {
    choose: async () =>
      guard(
        await p.select({ message: 'Which agent would you like to use?', options: agentOptions() }),
      ),
    configured: (path) => p.note(`Config: ${path}\nMCP sign-in is pending.`, 'Agent configured'),
    ready: ({ organization, agent, profileId, toolCount }) => {
      p.note(
        `Organization: ${organization}\nAgent: ${agent}\nProfile: ${profileId}\nDurin MCP: signed in and verified\nAvailable tools: ${toolCount}`,
        'Setup complete',
      );
      if (toolCount === 0)
        p.log.info(
          'No tools are currently available. Ask your administrator to enable a connection or grant access.',
        );
      p.outro(
        `Open ${agent} to use your approved tools. You can also use durin tools or connect an MCP client directly.`,
      );
    },
  },
  openAuthorization,
  stage: (message) => p.log.step(message),
});
