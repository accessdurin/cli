import { existsSync } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { runSetup } from './setup.js';
import { setupAccount } from './setup-account.js';

vi.mock('./setup-account.js', async (original) => {
  const actual = await original<typeof import('./setup-account.js')>();
  return { setupAccount: vi.fn<typeof actual.setupAccount>(actual.setupAccount) };
});
vi.mock('./onboard-organization.js', () => ({
  onboardOrganization: vi.fn<() => Promise<void>>(async () => {}),
}));
vi.mock('./agent-installed.js', () => ({ agentInstalled: () => true }));

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

describe('setup MCP host pinning', () => {
  const personal = `/mcp/u/${'a'.repeat(64)}`;
  const directories: string[] = [];
  afterEach(async () => {
    await Promise.all(
      directories.splice(0).map((path) => rm(path, { recursive: true, force: true })),
    );
  });

  const verifiedAccount = (appOrigin: string, endpoint: string) =>
    ({
      scope: {
        appOrigin,
        accountIssuer: 'https://auth.getdurin.com/',
        accountClientId: 'client_account',
        principalId: 'user_one',
      },
      identity: { principalId: 'user_one', organizationId: 'org_provider' },
      api: {
        workspace: async () => ({
          organization: { id: 'org_one', name: 'One' },
          session: { principalId: 'user_one', organizationId: 'org_provider' },
          membership: { principalId: 'user_one', active: true },
          settings: { environment: 'production' },
          mcp: { status: 'ready', url: endpoint },
        }),
        setupEndpoint: async () => {
          throw new Error('The endpoint is already ready.');
        },
      },
    }) as unknown as Awaited<ReturnType<typeof setupAccount>>;

  const setupWith = async (
    endpoint: string,
    options: { appOrigin?: string; development?: boolean; issuer?: string } = {},
  ) => {
    const appOrigin = options.appOrigin ?? 'https://app.getdurin.com';
    const directory = await mkdtemp(join(tmpdir(), 'durin-setup-hosts-'));
    directories.push(directory);
    const agentConfig = join(directory, 'claude.json');
    const requested: string[] = [];
    vi.mocked(setupAccount).mockResolvedValueOnce(verifiedAccount(appOrigin, endpoint));
    const request: typeof fetch = async (input) => {
      const url = new URL(input instanceof Request ? input.url : String(input));
      requested.push(url.host);
      return options.issuer && url.pathname.includes('oauth-protected-resource')
        ? Response.json({ resource: endpoint, authorization_servers: [options.issuer] })
        : new Response(null, { status: 503 });
    };
    const result = runSetup({
      appOrigin,
      development: options.development ?? false,
      directory,
      programPath: join(directory, 'missing-durin.js'),
      intent: 'login',
      request,
      agents: {
        claude: {
          name: 'Claude Code',
          configPath: agentConfig,
          format: 'jsonc',
          serverKey: 'mcpServers',
          urlKey: 'url',
          transport: { type: 'http' },
          bridge: 'arguments',
          bridgeTransport: { type: 'stdio' },
          authentication: 'Open Claude Code and use /mcp to authenticate Durin.',
        },
      },
      ui: {
        account: {
          intent: async () => 'login',
          authorize: async () => {},
        },
        organizations: { choose: async () => 'org_one', create: async () => 'One' },
        onboarding: {
          answer: async () => {
            throw new Error('Onboarding is complete.');
          },
          payment: async () => {},
        },
        agent: { choose: async () => 'claude', configured: () => {}, ready: () => {} },
        openAuthorization: async () => {},
        stage: () => {},
      },
    });
    const outcome = await result.then(
      () => null,
      (error: unknown) => error,
    );
    return {
      outcome,
      requested,
      savedProfile: existsSync(join(directory, 'profiles')),
      configuredAgent: existsSync(agentConfig),
    };
  };

  it('contacts the production MCP gateway named by the app', async () => {
    const { requested } = await setupWith(`https://mcp.getdurin.com${personal}`);
    expect(requested.length).toBeGreaterThan(0);
    expect(new Set(requested)).toEqual(new Set(['mcp.getdurin.com']));
  });

  it.each([
    ['a lookalike host', 'https://evil-durin.com', 'evil-durin.com'],
    ['a suffix lookalike host', 'https://getdurin.com.evil.test', 'getdurin.com.evil.test'],
    ['a subdomain that is not allowed', 'https://evil.getdurin.com', 'evil.getdurin.com'],
    ['another environment’s gateway', 'https://moria.mcp.getdurin.com', 'moria.mcp.getdurin.com'],
    ['a loopback host outside development', 'https://127.0.0.1:8443', '127.0.0.1'],
  ])(
    'stops before contacting, saving, or configuring anything when the app names an endpoint on %s',
    async (_case, origin, unexpected) => {
      const result = await setupWith(`${origin}${personal}`);
      expect(result.outcome).toBeInstanceOf(Error);
      expect(String(result.outcome)).toContain(unexpected);
      expect(String(result.outcome)).toContain('mcp.getdurin.com');
      expect(result.requested).toEqual([]);
      expect(result.savedProfile).toBe(false);
      expect(result.configuredAgent).toBe(false);
    },
  );

  it('contacts a loopback MCP gateway when development is enabled', async () => {
    const { requested } = await setupWith(`http://127.0.0.1:5272${personal}`, {
      appOrigin: 'http://127.0.0.1:5270',
      development: true,
    });
    expect(new Set(requested)).toEqual(new Set(['127.0.0.1:5272']));
  });

  it.each([
    ['a lookalike host', 'https://evil-durin.com', 'evil-durin.com'],
    [
      'a suffix lookalike host',
      'https://auth.getdurin.com.evil.test',
      'auth.getdurin.com.evil.test',
    ],
    ['a subdomain that is not allowed', 'https://login.getdurin.com', 'login.getdurin.com'],
    ['a loopback host outside development', 'https://localhost', 'localhost'],
  ])(
    'stops before saving or configuring anything when MCP discovery names a sign-in server on %s',
    async (_case, issuer, unexpected) => {
      const result = await setupWith(`https://mcp.getdurin.com${personal}`, { issuer });
      expect(result.outcome).toBeInstanceOf(Error);
      expect(String(result.outcome)).toContain(unexpected);
      expect(String(result.outcome)).toContain('auth.getdurin.com');
      expect(result.savedProfile).toBe(false);
      expect(result.configuredAgent).toBe(false);
    },
  );
});
