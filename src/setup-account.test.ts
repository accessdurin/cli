import { describe, expect, it } from 'vitest';
import { setupAccount } from './setup-account.js';
import { secureFetch } from './safe-fetch.js';
import { deploymentHostAllowlist } from './contracts/cli-host.js';

type Config = { issuer: string; providerOrigin: string };
const signIn = async (appOrigin: string, config: Config, development = false) => {
  const requested: string[] = [];
  const raw: typeof fetch = async (input) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    requested.push(`${url.host}${url.pathname}`);
    return url.pathname === '/auth/cli/config'
      ? Response.json({ clientId: 'client_account', appOrigin, ...config })
      : new Response(null, { status: 503 });
  };
  const outcome = await setupAccount({
    appOrigin,
    development,
    request: secureFetch(deploymentHostAllowlist({ appOrigin, development }), raw),
    clock: { now: () => 0, sleep: async () => {} },
    ui: { account: { authorize: async () => {} } } as never,
    intent: 'login',
  }).then(
    () => undefined,
    (error: unknown) => String(error),
  );
  return { outcome, requested };
};
const production = 'https://app.getdurin.com';
const staging = 'https://moria.app.getdurin.com';
const workos = 'https://api.workos.com';

describe('account sign-in hosts', () => {
  it.each([
    [production, { issuer: `${production}/`, providerOrigin: workos }, 'api.workos.com'],
    [
      staging,
      { issuer: 'https://resilient-tiger-64-staging.authkit.app/', providerOrigin: workos },
      'api.workos.com',
    ],
    [
      'https://durin.acme.test',
      { issuer: 'https://durin.acme.test/', providerOrigin: 'https://durin.acme.test' },
      'durin.acme.test',
    ],
  ])('starts device sign-in for %s on the account hosts it uses', async (app, config, host) => {
    const { outcome, requested } = await signIn(app, config);
    expect(outcome).toContain('Unable to start account sign-in.');
    expect(requested).toEqual([
      `${new URL(app).host}/auth/cli/config`,
      `${host}/user_management/authorize/device`,
    ]);
  });

  it('starts device sign-in on a loopback account API in development', async () => {
    const app = 'http://127.0.0.1:4567';
    const { outcome, requested } = await signIn(
      app,
      { issuer: `${app}/`, providerOrigin: app },
      true,
    );
    expect(outcome).toContain('Unable to start account sign-in.');
    expect(requested).toContain('127.0.0.1:4567/user_management/authorize/device');
  });

  it.each([
    'https://api.workos.com.evil.example',
    'https://evil-workos.com',
    'https://workos.com',
    'https://attacker.api.workos.com',
    'https://app.getdurin.com.evil.example',
    'https://127.0.0.1:4567',
  ])('never contacts an account API on %s, which the app does not use', async (providerOrigin) => {
    const { outcome, requested } = await signIn(production, {
      issuer: `${production}/`,
      providerOrigin,
    });
    expect(requested).toEqual(['app.getdurin.com/auth/cli/config']);
    expect(outcome).toContain(
      `The account sign-in service is on ${new URL(providerOrigin).hostname}`,
    );
    expect(outcome).toContain('Check --app-url, or ask your durin administrator.');
    expect(outcome).not.toMatch(/Durin/u);
  });

  it('gives a deployment without an allowlist only its own host for the account API', async () => {
    const { requested } = await signIn('https://durin.acme.test', {
      issuer: 'https://durin.acme.test/',
      providerOrigin: workos,
    });
    expect(requested).toEqual(['durin.acme.test/auth/cli/config']);
  });

  it.each([
    [production, 'https://api.workos.com.evil.example/'],
    [production, 'https://resilient-tiger-64-staging.authkit.app/'],
    [production, 'https://auth.getdurin.com/'],
    [staging, 'https://other-tenant.authkit.app/'],
    [staging, 'https://auth.getdurin.com/'],
  ])('stops %s before sign-in when the account issuer is %s', async (app, issuer) => {
    const { outcome, requested } = await signIn(app, { issuer, providerOrigin: workos });
    expect(requested).toEqual([`${new URL(app).host}/auth/cli/config`]);
    expect(outcome).toContain(`The account sign-in issuer is on ${new URL(issuer).hostname}`);
  });
});
