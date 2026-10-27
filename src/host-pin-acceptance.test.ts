// MAR-828 acceptance: Tess's QA repros of Seth's review of cli#8 (qa-seth-cli8.test.ts, reviewed
// head 5ede0ff). Each asserts the required behaviour; imports, fixtures and APIs are adapted.
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PassThrough } from 'node:stream';
import { setTimeout } from 'node:timers/promises';
import { expect, it, vi } from 'vitest';
import { secureFetch } from './safe-fetch.js';
import { signOutMcp } from './logout.js';
import { authorizeMcp, mcpIssuer } from './setup-mcp.js';
import { setupAccount } from './setup-account.js';
import { runSetup } from './setup.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { createProfile, FileProfiles } from './profiles.js';
import { profileConnection } from './profile-client.js';
import { cliOptions } from './cli-options.js';
import {
  deploymentHostAllowlist,
  profileHostAllowlist,
  untrustedCliHost,
} from './contracts/cli-host.js';

vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = async () => {};
    close = async () => {};
    listTools = async () => ({ tools: [{ name: 't' }] });
  },
}));

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

it('SETH-8-M1a: secureFetch refuses an https URL on a host the profile does not use', async () => {
  const { hosts, request } = recorder(() => new Response('{}'));
  const refused = await request(`https://${EVIL}/token`, { method: 'POST' }).catch(
    (error: unknown) => error,
  );
  expect(evilHit(hosts)).toEqual([]);
  expect(String(refused)).toContain(`durin didn’t contact ${EVIL}`);
});

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

it('SETH-8-M2a: a saved/created profile with a lookalike accountIssuer is rejected', () => {
  const { id: _id, version: _v, ...scope } = profile;
  let error: unknown;
  try {
    createProfile({ ...scope, accountIssuer: 'https://api.workos.com.evil.example/' });
  } catch (e) {
    error = e;
  }
  expect(error).toBeDefined();
  expect(String(error)).toContain('api.workos.com.evil.example');
});

it('SETH-8-M2b: device auth never contacts a lookalike providerOrigin from /auth/cli/config', async () => {
  const appOrigin = 'https://app.getdurin.com';
  const { hosts, request: raw } = recorder((url) => {
    // QA's issuer was https://api.workos.com/; production's own keeps this about providerOrigin.
    if (url.pathname === '/auth/cli/config')
      return Response.json({
        clientId: 'client',
        issuer,
        providerOrigin: `https://api.workos.com.${EVIL}`,
        appOrigin,
      });
    if (url.pathname.endsWith('/authorize/device'))
      return Response.json({
        device_code: 'd',
        user_code: 'U',
        verification_uri: `https://api.workos.com.${EVIL}/device`,
        expires_in: 600,
        interval: 1,
      });
    return new Response(null, { status: 500 });
  }, false);
  const outcome = await setupAccount({
    appOrigin,
    development: false,
    request: secureFetch(deploymentHostAllowlist({ appOrigin, development: false }), raw),
    clock: { now: () => 0, sleep: async () => {} } as never,
    ui: { account: { authorize: async () => {} } } as never,
    intent: 'sign-in' as never,
  }).catch((error: unknown) => error);
  expect(evilHit(hosts)).toEqual([]);
  expect(String(outcome)).toContain(`api.workos.com.${EVIL}`);
});

it('SETH-8-M2b: setup itself never contacts a lookalike providerOrigin, even unguarded', async () => {
  const appOrigin = 'https://app.getdurin.com';
  const { hosts, request } = recorder(
    (url) =>
      url.pathname === '/auth/cli/config'
        ? Response.json({
            clientId: 'client',
            issuer,
            providerOrigin: `https://api.workos.com.${EVIL}`,
            appOrigin,
          })
        : new Response(null, { status: 500 }),
    false,
  );
  await setupAccount({
    appOrigin,
    development: false,
    request,
    clock: { now: () => 0, sleep: async () => {} } as never,
    ui: { account: { authorize: async () => {} } } as never,
    intent: 'sign-in' as never,
  }).catch(() => undefined);
  expect(evilHit(hosts)).toEqual([]);
});

it('SETH-8-L: an MCP endpoint/issuer on a non-default port (:8443) is rejected', async () => {
  const production = { appOrigin: 'https://app.getdurin.com', development: false };
  expect(
    untrustedCliHost(
      'resource',
      `https://mcp.getdurin.com:8443/mcp/u/${'a'.repeat(64)}`,
      production,
    ),
  ).toBeDefined();
  expect(
    untrustedCliHost('mcpIssuer', 'https://auth.getdurin.com:8443/', production),
  ).toBeDefined();
  const r8443 = `https://mcp.getdurin.com:8443/mcp/u/${'a'.repeat(64)}`;
  const accepted = await mcpIssuer(
    r8443,
    async () => Response.json({ resource: r8443, authorization_servers: [issuer] }),
    production,
  ).then(
    () => true,
    () => false,
  );
  expect(accepted).toBe(false);
});

const workos = 'https://api.workos.com';
const selfHosted = 'https://durin.acme.test';
/** Runs setup, unguarded as the CLI calls it, against an app whose config names these hosts. */
const setupSignIn = async (appOrigin: string, accountIssuer: string, providerOrigin = workos) => {
  const { hosts, request } = recorder(
    (url) =>
      url.pathname === '/auth/cli/config'
        ? Response.json({ clientId: 'client', appOrigin, issuer: accountIssuer, providerOrigin })
        : new Response(null, { status: 503 }),
    false,
  );
  const outcome = await runSetup({
    appOrigin,
    development: false,
    directory: '/tmp/durin-unused-test',
    programPath: '/tmp/durin.js',
    intent: 'login',
    request,
    store: memoryStore(),
    clock: { now: () => 0, sleep: async () => {} },
    ui: { account: { authorize: async () => {} }, stage: () => {} } as never,
  }).then(
    () => 'finished',
    (error: unknown) => String(error),
  );
  return { hosts, outcome };
};
const configOnly = (appOrigin: string) => [`GET ${new URL(appOrigin).host}/auth/cli/config`];
const deviceSignIn = (appOrigin: string, provider: string) => [
  ...configOnly(appOrigin),
  `POST ${provider}/user_management/authorize/device`,
];

it.each([
  'https://auth.getdurin.com/',
  'https://auth.getdurin.com:443/',
  'https://AUTH.getdurin.com',
])('production signs accounts in with the account issuer %s on api.workos.com', async (value) => {
  const { hosts, outcome } = await setupSignIn('https://app.getdurin.com', value);
  expect(hosts).toEqual(deviceSignIn('https://app.getdurin.com', 'api.workos.com'));
  expect(outcome).toContain('Unable to start account sign-in.');
});

it.each([
  'https://auth.getdurin.com.evil.example/',
  'https://evil-auth.getdurin.com/',
  'https://login.auth.getdurin.com/',
  'https://getdurin.com/',
  'https://auth.getdurin.com:8443/',
  'https://resilient-tiger-64-staging.authkit.app/',
  'https://api.workos.com/',
])('production refuses the account issuer %s before sign-in', async (value) => {
  const { hosts, outcome } = await setupSignIn('https://app.getdurin.com', value);
  expect(hosts).toEqual(configOnly('https://app.getdurin.com'));
  expect(outcome).toContain(`The account sign-in issuer is on ${new URL(value).host}`);
  expect(outcome).toContain('uses auth.getdurin.com or app.getdurin.com');
});

it.each([
  ['https://app.getdurin.com', 'http://auth.getdurin.com/'],
  [selfHosted, 'http://acme-tenant.authkit.app/'],
  [selfHosted, 'http://api.workos.com/'],
])('%s refuses the account issuer %s, which is not HTTPS, before sign-in', async (app, value) => {
  const { hosts, outcome } = await setupSignIn(app, value);
  expect(hosts).toEqual(configOnly(app));
  expect(outcome).toContain('does not match this deployment');
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
  const { id: _id, version: _v, ...production } = profile;
  const scope = {
    ...production,
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
