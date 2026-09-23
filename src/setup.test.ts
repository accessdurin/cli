import { expect, it } from 'vitest';
import { runSetup } from './setup.js';

it('starts with login/signup and cannot configure an agent when native account authorization is unavailable', async () => {
  const events: string[] = [];
  await expect(
    runSetup({
      appOrigin: 'https://app.example.test',
      development: false,
      directory: '/tmp/durin-unused-test',
      programPath: '/tmp/durin.js',
      request: async () => new Response(null, { status: 503 }),
      ui: {
        account: {
          intent: async () => {
            events.push('choose-login-signup');
            return 'signup';
          },
          authorize: async () => {
            events.push('authorize');
          },
        },
        organizations: { choose: async () => 'org_one', create: async () => 'One' },
        onboarding: {
          answer: async () => {
            throw new Error('No account');
          },
          payment: async () => {},
        },
        agent: {
          choose: async () => {
            events.push('choose-agent');
            return 'claude';
          },
          configured: () => {
            events.push('configured');
          },
          ready: () => {
            events.push('ready');
          },
        },
        openAuthorization: async () => {},
        stage: () => {},
      },
    }),
  ).rejects.toThrow('Native account authentication');
  expect(events).toEqual(['choose-login-signup']);
});
