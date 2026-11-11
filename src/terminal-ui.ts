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
import { terminalText } from './terminal-text.js';
import { readCustomHarness } from './custom-harness.js';
import { endpointChecker, readChecker } from './hook-checker.js';

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
    p.note(`${action}\nDevice code: ${terminalText(prompt.code)}`, 'Account authorization');
    await openAuthorization(prompt.url);
  },
};

const chooseChecker: NonNullable<SetupUi['agent']['checker']> = async (agent) => {
  const endpoint = await detectedChecker(agent);
  const options = [
    ...(endpoint ? [{ value: 'endpoint', label: 'Use the installed Durin Endpoint helper' }] : []),
    {
      value: 'custom',
      label: 'Use a custom local checker',
      hint: 'Select a checker descriptor JSON file',
    },
    {
      value: 'skip',
      label: 'Skip local hook setup',
      hint: 'Existing hooks are preserved',
    },
  ];
  const choice = guard(await p.select({ message: 'Configure local pre-tool checks?', options }));
  if (choice === 'endpoint') return endpoint;
  return choice === 'custom'
    ? readChecker(await fileQuestion('Path to your local checker descriptor JSON'))
    : null;
};
const reportIntegrations: NonNullable<SetupUi['agent']['integrations']> = ({
  skills,
  hooks,
  custom,
}) => {
  p.log.info(
    `Durin skills installed: ${skills}. Local hooks: ${hooks ? 'configured; reopen the agent to activate' : 'not configured in this run; existing hooks retained'}.`,
  );
  if ([custom, hooks].every(Boolean))
    p.log.info('Wire the generated hook command into your custom harness before tool execution.');
  if (hooks)
    p.log.info(
      'For Codex, review and trust the hook in /hooks. Configuration does not verify hook execution or Endpoint enrollment.',
    );
};
const detectedChecker = (agent: import('./agents.js').AgentConfig) =>
  endpointChecker(agent.id ?? '');
const agentQuestions: SetupUi['agent'] = {
  choose: async () =>
    guard(
      await p.select({ message: 'Which agent would you like to use?', options: agentOptions() }),
    ),
  custom: async () =>
    readCustomHarness(await fileQuestion('Path to your custom harness descriptor JSON')),
  checker: chooseChecker,
  integrations: reportIntegrations,
  configured: (path) =>
    p.note(`Config: ${terminalText(path)}\nMCP sign-in is pending.`, 'Agent configured'),
  ready: ({ organization, agent, profileId, toolCount }) => {
    p.note(
      `Organization: ${terminalText(organization)}\nAgent: ${terminalText(agent)}\nProfile: ${profileId}\nDurin MCP: signed in and verified\nAvailable tools: ${toolCount}`,
      'Setup complete',
    );
    if (toolCount === 0)
      p.log.info(
        'No tools are currently available. Ask your administrator to enable a connection or grant access.',
      );
    p.outro(
      `Reopen ${terminalText(agent)} to load Durin tools and skills. You can also use durin tools or connect an MCP client directly.`,
    );
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
            label: terminalText(organization.name),
          })),
        }),
      ),
    create: organizationName,
  },
  onboarding: {
    answer,
    payment: async (organizationId) => {
      p.log.info(
        `Complete payment setup for ${terminalText(organizationId)} in the browser. Waiting for verified payment and provisioning.`,
      );
    },
  },
  agent: agentQuestions,
  openAuthorization,
  stage: (message) => p.log.step(message),
});

const fileQuestion = async (message: string): Promise<string> =>
  guard(
    await p.text({
      message,
      validate: (value) => (value?.trim() ? undefined : 'Enter a file path.'),
    }),
  );
