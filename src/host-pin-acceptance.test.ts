// MAR-828 acceptance: Tess's QA repros of Seth's review of cli#8 (qa-seth-cli8.test.ts, reviewed
// head 5ede0ff). Each asserts the required behaviour; imports, fixtures and APIs are adapted.
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PassThrough } from 'node:stream';
import { setTimeout } from 'node:timers/promises';
import { expect, it, vi } from 'vitest';
import { secureFetch } from './safe-fetch.js';
import { signOutMcp } from './logout.js';
import { authorizeMcp } from './setup-mcp.js';
import { runSetup } from './setup.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { createProfile, FileProfiles } from './profiles.js';
import { profileConnection } from './profile-client.js';
import { cliOptions } from './cli-options.js';
import type { AgentConfig } from './agents.js';
import type { SetupUi } from './ports/cli-setup.js';
import { profileHostAllowlist } from './contracts/cli-host.js';

vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = async () => {};
    close = async () => {};
    listTools = async () => ({ tools: [{ name: 't' }] });
  },
}));
// Setup's last steps run on this machine, not against a host: the agent, its installed runtime
// and the saved bridge's own check.
vi.mock('./agent-installed.js', () => ({ agentInstalled: () => true }));
vi.mock('./install-runtime.js', () => ({
  installRuntime: async (directory: string) => `${directory}/runtime/cli.mjs`,
}));
vi.mock('./verify-bridge.js', () => ({ verifySavedBridge: async () => 1 }));

const EVIL = 'evil.example';
const resource = `https://mcp.getdurin.com/mcp/u/${'a'.repeat(64)}`;
const issuer = 'https://auth.getdurin.com/';
// QA used https://api.workos.com/; production signs both MCP grants and accounts on
// auth.getdurin.com.
const profile = createProfile({
  appOrigin: 'https://app.getdurin.com',
  accountIssuer: issuer,
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource,
  mcpIssuer: issuer,
  development: false,
});
const memoryStore = (values = new Map<string, string>()) => ({
  read: async (k: string) => values.get(k) ?? null,
  write: async (k: string, v: string) => {
    values.set(k, v);
  },
  remove: async (k: string) => {
    values.delete(k);
  },
});
const memoryMarker = () => {
  let p = false;
  return {
    pending: async () => p,
    mark: async () => {
      p = true;
    },
    clear: async () => {
      p = false;
    },
  };
};
const noPending = { entries: async () => [], add: async () => {}, replace: async () => {} };
/** `guarded` wraps the recorder as the CLI's callers do; unguarded shows the code guards itself. */
const recorder = (
  route: (url: URL, init?: RequestInit) => Response | Promise<Response>,
  guarded = true,
) => {
  const hosts: string[] = [];
  const raw: typeof fetch = async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input));
    hosts.push(`${init?.method ?? 'GET'} ${url.host}${url.pathname}`);
    return route(url, init);
  };
  return { hosts, request: guarded ? secureFetch(profileHostAllowlist(profile), raw) : raw };
};
const evilHit = (hosts: string[]) => hosts.filter((h) => h.includes(EVIL));
const signedIn = async (values: Map<string, string>) => {
  const provider = new McpOAuthProvider({ profile, store: memoryStore(values) });
  await provider.saveClientInformation({ client_id: 'client_one', issuer });
  await provider.saveTokens({
    access_token: 'synthetic-access',
    refresh_token: 'synthetic-refresh',
    token_type: 'Bearer',
    issuer,
  });
  return provider;
};

it.each([true, false])(
  'SETH-8-M1b: logout never sends tokens to a metadata-supplied revocation_endpoint on another host (caller guards fetch: %s)',
  async (guarded) => {
    const values = new Map<string, string>();
    await signedIn(values);
    const { hosts, request } = recorder(
      (url) =>
        url.pathname.includes('.well-known')
          ? Response.json({
              issuer,
              authorization_endpoint: `${issuer}authorize`,
              token_endpoint: `${issuer}token`,
              response_types_supported: ['code'],
              revocation_endpoint: `https://${EVIL}/revoke`,
            })
          : new Response(null, { status: 200 }),
      guarded,
    );
    await signOutMcp({
      profile,
      store: memoryStore(values),
      marker: memoryMarker(),
      request: async (i, init) => request(i, init),
    }).catch(() => undefined);
    expect(evilHit(hosts)).toEqual([]);
    expect(hosts).toEqual(['GET auth.getdurin.com/.well-known/oauth-authorization-server']);
  },
);

type Reply = readonly [path: string, reply: (init?: RequestInit) => Response];
const signInReplies: readonly Reply[] = [
  ['oauth-protected-resource', () => Response.json({ resource, authorization_servers: [issuer] })],
  [
    '.well-known',
    () =>
      Response.json({
        issuer,
        scopes_supported: ['email'],
        authorization_endpoint: `${issuer}authorize`,
        token_endpoint: `https://${EVIL}/token`,
        registration_endpoint: `https://${EVIL}/register`,
        response_types_supported: ['code'],
        code_challenge_methods_supported: ['S256'],
        token_endpoint_auth_methods_supported: ['none'],
        authorization_response_iss_parameter_supported: true,
      }),
  ],
  [
    '/register',
    (init) => Response.json({ ...JSON.parse(String(init?.body)), client_id: 'mcp_public_client' }),
  ],
];
const tokenReply: Reply = [
  '',
  () =>
    Response.json({
      access_token: 'a',
      refresh_token: 'r',
      token_type: 'Bearer',
      expires_in: 3600,
    }),
];
it.each([true, false])(
  'SETH-8-M1c: sign-in never sends the code/registration to metadata-supplied token/registration endpoints on another host (caller guards fetch: %s)',
  async (guarded) => {
    const { hosts, request } = recorder(
      (url, init) =>
        (signInReplies.find(([path]) => url.pathname.includes(path)) ?? tokenReply)[1](init),
      guarded,
    );
    const outcome = await authorizeMcp({
      profile,
      store: memoryStore(),
      marker: memoryMarker(),
      pending: noPending,
      request,
      open: async (value) => {
        const a = new URL(value);
        const cb = new URL(a.searchParams.get('redirect_uri')!);
        cb.search = new URLSearchParams({
          code: 'synthetic-code',
          state: a.searchParams.get('state')!,
          iss: issuer,
        }).toString();
        await fetch(cb.href);
      },
    }).catch((error: unknown) => error);
    expect(evilHit(hosts)).toEqual([]);
    expect(String(outcome)).toContain(`durin didn’t contact ${EVIL}`);
  },
);

it('SETH-8-M1d: a 401 naming resource_metadata on another host is not followed by the bridge', async () => {
  const values = new Map<string, string>();
  const provider = await signedIn(values);
  const { hosts, request } = recorder((url) => {
    if (url.host === 'mcp.getdurin.com')
      return new Response(null, {
        status: 401,
        headers: {
          'www-authenticate': `Bearer resource_metadata="https://${EVIL}/.well-known/oauth-protected-resource"`,
        },
      });
    return Response.json({ resource, authorization_servers: [issuer] });
  });
  const input = new PassThrough();
  const output = new PassThrough();
  const bridge = new McpStdioBridge({
    resource,
    authorization: provider,
    fetch: request,
    input,
    output,
    development: false,
  });
  await bridge.start();
  try {
    input.write(
      `${JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'x', arguments: {} } })}\n`,
    );
    await setTimeout(500);
  } finally {
    await bridge.close();
  }
  expect(evilHit(hosts)).toEqual([]);
});

it('gives a saved profile’s MCP connection a fetch that reaches only that profile’s hosts', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-host-pin-'));
  try {
    const profiles = new FileProfiles(directory);
    await profiles.save(profile);
    await profiles.activate(profile.id);
    const sent: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      sent.push(new URL(input instanceof Request ? input.url : String(input)).host);
      return new Response('{}');
    });
    const connection = await profileConnection(
      cliOptions(['mcp', 'serve', '--config-dir', directory]),
    );
    await Promise.all(
      [`https://${EVIL}/token`, 'https://app.getdurin.com/token'].map((target) =>
        expect(connection.fetch(target, { method: 'POST' })).rejects.toThrow('didn’t contact'),
      ),
    );
    await connection.fetch(`${issuer}token`, { method: 'POST' });
    await connection.fetch(resource, { method: 'POST' });
    expect(sent).toEqual(['auth.getdurin.com', 'mcp.getdurin.com']);
  } finally {
    vi.restoreAllMocks();
    await rm(directory, { recursive: true, force: true });
  }
});

const workos = 'https://api.workos.com';
const selfHosted = 'https://durin.acme.test';
const production = 'https://app.getdurin.com';
/** The live issuer of production's MCP grants and accounts, as its openid-configuration names it. */
const productionIssuer = 'https://auth.getdurin.com';
type Route = Parameters<typeof recorder>[0];
const agents = (directory: string): Readonly<Record<string, AgentConfig>> => ({
  claude: {
    name: 'Claude Code',
    configPath: join(directory, 'claude.json'),
    format: 'jsonc',
    serverKey: 'mcpServers',
    urlKey: 'url',
    transport: { type: 'http' },
    bridge: 'arguments',
    bridgeTransport: { type: 'stdio' },
    authentication: 'Open Claude Code and use /mcp to authenticate durin.',
  },
});
/** Plays the browser: approves MCP sign-in by calling the CLI's loopback callback directly. */
const approveInBrowser = async (value: string) => {
  const authorization = new URL(value);
  const callback = new URL(authorization.searchParams.get('redirect_uri')!);
  callback.search = new URLSearchParams({
    code: 'synthetic-code',
    state: authorization.searchParams.get('state')!,
    iss: productionIssuer,
  }).toString();
  await fetch(callback.href);
};
/**
 * How setup's requests leave: injected into runSetup, through the global fetch the CLI uses
 * (both recorded, never sent), or over the real network to local servers.
 */
type Transport = 'injected' | 'global' | 'network';
/** Picks the first organization and the claude agent; records each profile setup finishes. */
const setupUi = (ready: string[]): SetupUi => ({
  account: { intent: async () => 'login', authorize: async () => {} },
  organizations: { choose: async ([first]) => first!.id, create: async () => 'One' },
  onboarding: {
    answer: async () => {
      throw new Error('Onboarding is complete.');
    },
    payment: async () => {},
  },
  agent: {
    choose: async () => 'claude',
    configured: () => {},
    ready: ({ profileId }) => {
      ready.push(profileId);
    },
  },
  openAuthorization: approveInBrowser,
  stage: () => {},
});
/** Records every request setup sends through `transport`. */
const setupRequests = (route: Route, transport: Transport) => {
  const { hosts, request } = recorder(route, false);
  if (transport === 'global') vi.spyOn(globalThis, 'fetch').mockImplementation(request);
  return { hosts, options: transport === 'injected' ? { request } : {} };
};
type SetupRun = {
  readonly appOrigin: string;
  readonly route?: Route;
  readonly development?: boolean;
  readonly transport?: Transport;
};
/** Runs setup, unguarded as the CLI calls it, in a fresh config directory. */
const setupRun = async (input: SetupRun) => {
  const run = {
    route: () => new Response(null, { status: 503 }),
    development: false,
    transport: 'injected' as const,
    ...input,
  };
  const { hosts, options } = setupRequests(run.route, run.transport);
  const directory = await mkdtemp(join(tmpdir(), 'durin-host-pin-'));
  const ready: string[] = [];
  try {
    const outcome = await runSetup({
      appOrigin: run.appOrigin,
      development: run.development,
      directory,
      programPath: join(directory, 'durin.js'),
      intent: 'login',
      ...options,
      store: memoryStore(),
      clock: { now: () => 0, sleep: async () => {} },
      agents: agents(directory),
      ui: setupUi(ready),
    }).then(
      () => 'finished',
      (error: unknown) => String(error),
    );
    return { hosts, outcome, saved: await new FileProfiles(directory).list(), ready };
  } finally {
    vi.restoreAllMocks();
    await rm(directory, { recursive: true, force: true });
  }
};
/** An app whose /auth/cli/config names these account hosts, and which serves nothing else. */
const setupSignIn = (appOrigin: string, accountIssuer: string, providerOrigin = workos) =>
  setupRun({
    appOrigin,
    route: (url) =>
      url.pathname === '/auth/cli/config'
        ? Response.json({ clientId: 'client', appOrigin, issuer: accountIssuer, providerOrigin })
        : new Response(null, { status: 503 }),
  });
const configOnly = (appOrigin: string) => [`GET ${new URL(appOrigin).host}/auth/cli/config`];
const deviceSignIn = (appOrigin: string, provider: string) => [
  ...configOnly(appOrigin),
  `POST ${provider}/user_management/authorize/device`,
];

/** Fake production: the app, WorkOS and the MCP gateway and sign-in server, so setup finishes. */
const productionDeployment = (
  changes: { endpoint?: string; mcpIssuer?: string; registration?: string } = {},
): Route => {
  const endpoint = changes.endpoint ?? resource;
  const session = {
    mode: 'workos',
    principalId: 'user_one',
    name: 'One',
    email: 'one@example.test',
    role: 'admin',
    organizationId: 'org_one',
    organizations: [{ id: 'org_one', name: 'One' }],
  };
  const routes: Readonly<Record<string, (init?: RequestInit) => Response>> = {
    '/auth/cli/config': () =>
      Response.json({
        clientId: 'client_account',
        appOrigin: production,
        issuer: productionIssuer,
        providerOrigin: workos,
      }),
    '/user_management/authorize/device': () =>
      Response.json({
        device_code: 'device',
        user_code: 'CODE',
        verification_uri: `${productionIssuer}/device`,
        expires_in: 600,
        interval: 1,
      }),
    '/user_management/authenticate': (init) =>
      Response.json({
        access_token: 'account-access',
        refresh_token: 'account-refresh',
        organization_id: new URLSearchParams(String(init?.body)).get('organization_id'),
        user: { id: 'user_one' },
      }),
    '/api/v1/session': () => Response.json(session),
    '/api/v1/onboarding': () =>
      Response.json({
        organizationId: 'org_one',
        journey: 'existing',
        next: '/connections',
        allowed: true,
        profile: null,
        step: 'complete',
        tenantReady: true,
      }),
    '/api/v1/me/bootstrap': () =>
      Response.json({
        organization: { id: 'org_internal', name: 'One', environment: 'production' },
        membership: { principalId: 'user_one', role: 'admin', active: true, revocationEpoch: 0 },
        entitlements: { organizationId: 'org_internal', package: 'standard' },
        session,
        mcp: { status: 'ready', url: endpoint },
        settings: { retention: 'metadata_only', environment: 'production' },
      }),
    [`/.well-known/oauth-protected-resource${new URL(endpoint).pathname}`]: () =>
      Response.json({
        resource: endpoint,
        authorization_servers: [changes.mcpIssuer ?? productionIssuer],
      }),
    '/.well-known/oauth-authorization-server': () =>
      Response.json({
        issuer: productionIssuer,
        scopes_supported: ['email'],
        authorization_endpoint: `${productionIssuer}/authorize`,
        token_endpoint: `${productionIssuer}/token`,
        registration_endpoint: changes.registration ?? `${productionIssuer}/register`,
        response_types_supported: ['code'],
        code_challenge_methods_supported: ['S256'],
        token_endpoint_auth_methods_supported: ['none'],
        authorization_response_iss_parameter_supported: true,
      }),
    '/register': (init) =>
      Response.json({ ...JSON.parse(String(init?.body)), client_id: 'mcp_public_client' }),
    '/token': () =>
      Response.json({
        access_token: 'mcp-access',
        refresh_token: 'mcp-refresh',
        token_type: 'Bearer',
        expires_in: 3600,
      }),
  };
  return (url, init) => routes[url.pathname]?.(init) ?? new Response(null, { status: 404 });
};
const accountSignIn = [
  ...deviceSignIn(production, 'api.workos.com'),
  'POST api.workos.com/user_management/authenticate',
  'GET app.getdurin.com/api/v1/session',
  'POST api.workos.com/user_management/authenticate',
  'GET app.getdurin.com/api/v1/session',
  'GET app.getdurin.com/api/v1/onboarding',
  'GET app.getdurin.com/api/v1/me/bootstrap',
];
const mcpDiscovery = `GET mcp.getdurin.com/.well-known/oauth-protected-resource${new URL(resource).pathname}`;
/** After the profile is saved: MCP sign-in, with the browser step in between. */
const mcpSignIn = [
  mcpDiscovery,
  'GET auth.getdurin.com/.well-known/oauth-authorization-server',
  'POST auth.getdurin.com/register',
  'POST auth.getdurin.com/token',
];

it('signs in to production end to end, contacting only its hosts, and saves the profile', async () => {
  const { hosts, outcome, saved, ready } = await setupRun({
    appOrigin: production,
    route: productionDeployment(),
  });
  expect(outcome).toBe('finished');
  expect(hosts).toEqual([...accountSignIn, mcpDiscovery, ...mcpSignIn]);
  expect(saved).toMatchObject([
    {
      appOrigin: production,
      accountIssuer: productionIssuer,
      mcpIssuer: productionIssuer,
      resource,
    },
  ]);
  expect(ready).toEqual([saved[0]!.id]);
});

it('SETH-8-M1a: setup never contacts a host the profile does not use, even when its sign-in server names one', async () => {
  const { hosts, outcome, ready } = await setupRun({
    appOrigin: production,
    route: productionDeployment({ registration: `https://${EVIL}/register` }),
  });
  expect(evilHit(hosts)).toEqual([]);
  expect(hosts).toEqual([...accountSignIn, mcpDiscovery, ...mcpSignIn.slice(0, 2)]);
  expect(outcome).toContain(`durin didn’t contact ${EVIL}`);
  expect(ready).toEqual([]);
});

it.each([
  ['MCP endpoint', { endpoint: `https://mcp.getdurin.com:8443${new URL(resource).pathname}` }, []],
  ['MCP sign-in server', { mcpIssuer: 'https://auth.getdurin.com:8443' }, [mcpDiscovery]],
])(
  'SETH-8-L: setup refuses an %s on a non-default port (:8443) before contacting it or saving a profile',
  async (role, changes, discovery) => {
    const { hosts, outcome, saved } = await setupRun({
      appOrigin: production,
      route: productionDeployment(changes),
    });
    expect(hosts).toEqual([...accountSignIn, ...discovery]);
    expect(outcome).toContain(`The ${role} is on `);
    expect(outcome).toContain(':8443');
    expect(saved).toEqual([]);
  },
);

it('SETH-8-M2a: a lookalike accountIssuer is refused from /auth/cli/config and in a saved profile', async () => {
  const lookalike = `https://api.workos.com.${EVIL}/`;
  const { hosts, outcome } = await setupSignIn(production, lookalike);
  expect(hosts).toEqual(configOnly(production));
  expect(outcome).toContain(`The account sign-in issuer is on api.workos.com.${EVIL}`);
  // A profile saved before this check, or edited since, is refused when a command loads it.
  const directory = await mkdtemp(join(tmpdir(), 'durin-host-pin-'));
  const sent = vi.spyOn(globalThis, 'fetch');
  try {
    await writeFile(join(directory, 'active'), `${profile.id}\n`);
    await mkdir(join(directory, 'profiles'));
    await writeFile(
      join(directory, 'profiles', `${profile.id}.json`),
      JSON.stringify({ ...profile, accountIssuer: lookalike }),
    );
    await expect(new FileProfiles(directory).load()).rejects.toThrow(`api.workos.com.${EVIL}`);
    await expect(
      profileConnection(cliOptions(['tools', 'list', '--config-dir', directory])),
    ).rejects.toThrow('No valid organization profile');
    expect(sent).not.toHaveBeenCalled();
  } finally {
    vi.restoreAllMocks();
    await rm(directory, { recursive: true, force: true });
  }
});

it.each(['global', 'injected'] as const)(
  'SETH-8-M2b: setup never contacts a lookalike providerOrigin from /auth/cli/config (fetch: %s)',
  async (transport) => {
    const { hosts, outcome } = await setupRun({
      appOrigin: production,
      transport,
      route: (url) =>
        url.pathname === '/auth/cli/config'
          ? Response.json({
              clientId: 'client',
              issuer,
              providerOrigin: `https://api.workos.com.${EVIL}`,
              appOrigin: production,
            })
          : Response.json({
              device_code: 'd',
              user_code: 'U',
              verification_uri: `https://api.workos.com.${EVIL}/device`,
              expires_in: 600,
              interval: 1,
            }),
    });
    expect(evilHit(hosts)).toEqual([]);
    expect(hosts).toEqual(configOnly(production));
    expect(outcome).toContain(`The account sign-in service is on api.workos.com.${EVIL}`);
  },
);

it.each([
  'https://auth.getdurin.com/',
  'https://auth.getdurin.com:443/',
  'https://AUTH.getdurin.com',
  productionIssuer,
])('production signs accounts in with the account issuer %s on api.workos.com', async (value) => {
  const { hosts, outcome } = await setupSignIn(production, value);
  expect(hosts).toEqual(deviceSignIn(production, 'api.workos.com'));
  expect(outcome).toContain('Unable to start account sign-in.');
});

it.each([
  'https://app.getdurin.com/',
  'https://auth.getdurin.com.evil.example/',
  'https://evil-auth.getdurin.com/',
  'https://login.auth.getdurin.com/',
  'https://getdurin.com/',
  'https://auth.getdurin.com:8443/',
  'https://аuth.getdurin.com/',
  'https://resilient-tiger-64-staging.authkit.app/',
  'https://api.workos.com/',
])('production refuses the account issuer %s before sign-in', async (value) => {
  const { hosts, outcome } = await setupSignIn(production, value);
  expect(hosts).toEqual(configOnly(production));
  expect(outcome).toContain(`The account sign-in issuer is on ${new URL(value).host}`);
  expect(outcome).toContain('but the durin app at app.getdurin.com uses auth.getdurin.com.');
});

it.each(['https://auth.getdurin.com/x', 'https://auth.getdurin.com//'])(
  'production refuses the account issuer %s, which is not exactly its issuer, before sign-in',
  async (value) => {
    const { hosts, outcome } = await setupSignIn(production, value);
    expect(hosts).toEqual(configOnly(production));
    expect(outcome).toContain(
      'The account sign-in issuer must be exactly auth.getdurin.com over HTTPS',
    );
  },
);

it.each([
  [
    'https://auth.getdurin.com/',
    [
      'GET app.getdurin.com/auth/cli/config',
      'POST api.workos.com/user_management/authorize/device',
    ],
    'Unable to start account sign-in.',
  ],
  [
    'https://acme-tenant.authkit.app/',
    ['GET app.getdurin.com/auth/cli/config'],
    'The account sign-in issuer is on acme-tenant.authkit.app, but the durin app at app.getdurin.com uses auth.getdurin.com.',
  ],
])(
  '--app-url https://app.getdurin.com. is production, whose config names it without the dot, for the account issuer %s',
  async (value, contacted, message) => {
    const { appOrigin } = cliOptions(['login', '--app-url', 'https://app.getdurin.com.']);
    expect(appOrigin).toBe(production);
    // The fake app's config names appOrigin exactly as the live one does: without the dot.
    const { hosts, outcome } = await setupSignIn(appOrigin, value);
    expect(hosts).toEqual(contacted);
    expect(outcome).toContain(message);
    expect(outcome).not.toContain('WorkOS issuer');
  },
);

it('treats a lookalike of the production app host as a self-hosted app, never as production', async () => {
  const app = new URL('https://аpp.getdurin.com').origin;
  const { hosts, outcome } = await setupSignIn(app, 'https://auth.getdurin.com/');
  expect(hosts).toEqual(configOnly(app));
  expect(outcome).toContain(`The account sign-in issuer is on auth.getdurin.com`);
  expect(outcome).toContain(`uses ${new URL(app).host} or a WorkOS issuer`);
});

it.each([
  ['https://app.getdurin.com', 'http://auth.getdurin.com/'],
  ['https://app.getdurin.com', 'http://auth.getdurin.com:443/'],
  ['https://app.getdurin.com', 'https://user@auth.getdurin.com/'],
  ['https://app.getdurin.com', `https://auth.getdurin.com@${EVIL}/`],
  ['https://app.getdurin.com', 'https://auth.getdurin.com/?tenant=evil'],
  [selfHosted, 'http://acme-tenant.authkit.app/'],
  [selfHosted, 'http://api.workos.com/'],
])(
  '%s refuses the account issuer %s, which is not a plain HTTPS address, before sign-in',
  async (app, value) => {
    const { hosts, outcome } = await setupSignIn(app, value);
    expect(hosts).toEqual(configOnly(app));
    expect(outcome).toContain('does not match this deployment');
  },
);

it.each([
  ['https://127.0.0.1:8443', issuer, [], 'durin didn’t contact 127.0.0.1:8443.'],
  [production, 'https://localhost/', configOnly(production), 'issuer is on localhost'],
])(
  'without --dev, setup at %s with the account issuer %s refuses loopback',
  async (app, value, contacted, message) => {
    const { hosts, outcome } = await setupSignIn(app, value);
    expect(hosts).toEqual(contacted);
    expect(outcome).toContain(message);
    expect(outcome).toContain('Local addresses are only allowed with --dev.');
  },
);

/** A real HTTP server on 127.0.0.1 that records every request it receives. */
const localServer = async (reply: (response: ServerResponse) => void) => {
  const hits: string[] = [];
  const server = createServer((request, response) => {
    hits.push(`${request.method} ${request.url}`);
    reply(response);
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;
  const close = () => {
    server.closeAllConnections();
    return new Promise<void>((resolve) => server.close(() => resolve()));
  };
  return { origin: `http://127.0.0.1:${port}`, hits, close };
};

it('never follows a redirect from /auth/cli/config, even to another local server with --dev', async () => {
  const target = await localServer((response) => {
    response.writeHead(503).end();
  });
  const app = await localServer((response) => {
    response.writeHead(302, { location: `${target.origin}/auth/cli/config` }).end();
  });
  try {
    const { outcome, saved } = await setupRun({
      appOrigin: app.origin,
      development: true,
      transport: 'network',
    });
    expect(outcome).toContain('fetch failed');
    expect(app.hits).toEqual(['GET /auth/cli/config']);
    expect(target.hits).toEqual([]);
    expect(saved).toEqual([]);
  } finally {
    await Promise.all([app.close(), target.close()]);
  }
});

it.each([
  'https://acme-tenant.authkit.app/',
  'https://resilient-tiger-64-staging.authkit.app/',
  'https://acme-tenant.authkit.app:443',
  'https://api.workos.com/user_management/client_acme',
])(
  'a self-hosted app with the WorkOS account issuer %s signs in on api.workos.com',
  async (value) => {
    const { hosts, outcome } = await setupSignIn(selfHosted, value);
    expect(hosts).toEqual(deviceSignIn(selfHosted, 'api.workos.com'));
    expect(outcome).toContain('Unable to start account sign-in.');
  },
);

it.each(['https://api.workos.com:8443', `https://api.workos.com.${EVIL}`, 'https://workos.com'])(
  'a self-hosted app with a WorkOS account issuer never contacts the account API %s',
  async (providerOrigin) => {
    const { hosts, outcome } = await setupSignIn(
      selfHosted,
      'https://acme-tenant.authkit.app/',
      providerOrigin,
    );
    expect(hosts).toEqual(configOnly(selfHosted));
    expect(outcome).toContain(`The account sign-in service is on ${new URL(providerOrigin).host}`);
    expect(outcome).toContain('uses api.workos.com or durin.acme.test');
  },
);

it('a self-hosted app with its own account issuer signs in on its own host only', async () => {
  const { hosts, outcome } = await setupSignIn(selfHosted, `${selfHosted}/`, selfHosted);
  expect(hosts).toEqual(deviceSignIn(selfHosted, 'durin.acme.test'));
  expect(outcome).toContain('Unable to start account sign-in.');
});

it('a self-hosted app with its own account issuer never contacts api.workos.com', async () => {
  const { hosts, outcome } = await setupSignIn(selfHosted, `${selfHosted}/`);
  expect(hosts).toEqual(configOnly(selfHosted));
  expect(outcome).toContain('The account sign-in service is on api.workos.com');
  expect(outcome).toContain('only when its account sign-in issuer is a WorkOS issuer');
});

it.each([
  'https://api.workos.com.evil.example/',
  'https://authkit.app.evil.example/',
  'https://acme-tenant.authkit.app.evil.example/',
  'https://authkit.app/',
  'https://attacker.acme-tenant.authkit.app/',
  'https://acme-tenant.authkit.app:8443/',
  'https://api.workos.com:8443/',
  'https://auth.getdurin.com/',
  `https://${EVIL}/`,
])('a self-hosted app refuses the account issuer %s before sign-in', async (value) => {
  const { hosts, outcome } = await setupSignIn(selfHosted, value);
  expect(hosts).toEqual(configOnly(selfHosted));
  expect(outcome).toContain(`The account sign-in issuer is on ${new URL(value).host}`);
  expect(outcome).toContain('uses durin.acme.test or a WorkOS issuer');
});

it('keeps a self-hosted profile with a WorkOS account issuer, and refuses a lookalike', async () => {
  const { id: _id, version: _v, ...binding } = profile;
  const scope = {
    ...binding,
    appOrigin: selfHosted,
    accountIssuer: 'https://acme-tenant.authkit.app/',
    resource: `${selfHosted}/mcp/u/${'a'.repeat(64)}`,
    mcpIssuer: `${selfHosted}/`,
  };
  const directory = await mkdtemp(join(tmpdir(), 'durin-host-pin-'));
  try {
    const profiles = new FileProfiles(directory);
    const saved = createProfile(scope);
    await profiles.save(saved);
    expect(await profiles.load(saved.id)).toEqual(saved);
    expect(profileHostAllowlist(saved).hosts).toEqual(['durin.acme.test']);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
  expect(() =>
    createProfile({ ...scope, accountIssuer: 'https://acme-tenant.authkit.app.evil.example/' }),
  ).toThrow('acme-tenant.authkit.app.evil.example');
});
