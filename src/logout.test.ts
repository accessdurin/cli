import { expect, it } from 'vitest';
import { signOutMcp } from './logout.js';
import { createProfile } from './profiles.js';
import { McpOAuthProvider } from './mcp-oauth.js';

const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://account.example.test',
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource: `https://mcp.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://issuer.example.test',
  development: false,
});
const memoryStore = (values: Map<string, string>) => ({
  read: async (key: string) => values.get(key) ?? null,
  write: async (key: string, value: string) => {
    values.set(key, value);
  },
  remove: async (key: string) => {
    values.delete(key);
  },
});
const memoryMarker = (state = { pending: false, marks: 0 }) => ({
  state,
  pending: async () => state.pending,
  mark: async () => {
    state.pending = true;
    state.marks += 1;
  },
  clear: async () => {
    state.pending = false;
  },
});
const signedIn = async (values: Map<string, string>): Promise<McpOAuthProvider> => {
  const provider = new McpOAuthProvider({ profile, store: memoryStore(values) });
  await provider.saveClientInformation({ client_id: 'client_one', issuer: profile.mcpIssuer });
  await provider.saveTokens({
    access_token: 'synthetic-access',
    refresh_token: 'synthetic-refresh',
    token_type: 'Bearer',
    issuer: profile.mcpIssuer,
  });
  return provider;
};
const authorizationServer =
  (revoke: (init?: RequestInit) => Response): typeof fetch =>
  async (url, init) =>
    String(url).includes('.well-known')
      ? Response.json({
          issuer: profile.mcpIssuer,
          authorization_endpoint: 'https://issuer.example.test/authorize',
          token_endpoint: 'https://issuer.example.test/token',
          response_types_supported: ['code'],
          revocation_endpoint: 'https://issuer.example.test/revoke',
        })
      : revoke(init);

it('revokes this profile grant where supported and removes only its local credentials', async () => {
  const values = new Map<string, string>([['unrelated', 'keep']]);
  await signedIn(values);
  const revoked: string[] = [];
  const request = authorizationServer((init) => {
    revoked.push(new URLSearchParams(String(init?.body)).get('token')!);
    return new Response(null, { status: 200 });
  });
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(revoked).toEqual(['synthetic-refresh', 'synthetic-access']);
  expect([...values.entries()]).toEqual([['unrelated', 'keep']]);
});

it('keeps the grant when revocation fails so that logout can retry it', async () => {
  const values = new Map<string, string>();
  const provider = await signedIn(values);
  let status = 503;
  const request = authorizationServer(() => new Response(null, { status }));
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'failed',
    local: 'retained',
  });
  expect(await provider.tokens()).toMatchObject({ refresh_token: 'synthetic-refresh' });
  status = 200;
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

it('clears saved credentials that cannot be read, since they cannot be revoked', async () => {
  const values = new Map<string, string>([[`mcp:${profile.id}:tokens`, 'not json']]);
  const request = authorizationServer(() => new Response(null, { status: 500 }));
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

const networkFailure = (code: string) => () => {
  throw new TypeError('fetch failed', { cause: Object.assign(new Error(code), { code }) });
};
const answering =
  (answer: () => Response): typeof fetch =>
  async () =>
    answer();
const garbled = (body: string, type: string) => () =>
  new Response(body, { status: 200, headers: { 'content-type': type } });
/** Failures after which the authorization server may still honour the grant. */
const unanswered = [
  ['a busy server', authorizationServer(() => new Response(null, { status: 503 }))],
  ['rate limiting', authorizationServer(() => new Response(null, { status: 429 }))],
  ['a refused connection', authorizationServer(networkFailure('ECONNREFUSED'))],
  ['a reset connection', authorizationServer(networkFailure('UND_ERR_SOCKET'))],
  ['an unresolvable host', authorizationServer(networkFailure('ENOTFOUND'))],
  ['an untrusted certificate', authorizationServer(networkFailure('DEPTH_ZERO_SELF_SIGNED_CERT'))],
  [
    'an unclassified fetch failure',
    authorizationServer(() => {
      throw new TypeError('fetch failed');
    }),
  ],
  [
    'a timeout',
    authorizationServer(() => {
      throw new DOMException('The operation was aborted due to timeout', 'TimeoutError');
    }),
  ],
  [
    'an HTML page instead of metadata',
    answering(garbled('<html>Sign in to Wi-Fi</html>', 'text/html')),
  ],
  ['truncated metadata', answering(garbled('{"issuer":"https://issuer', 'application/json'))],
] as const;
it.each(unanswered)('keeps the grant after %s', async (_name, request) => {
  const values = new Map<string, string>();
  await signedIn(values);
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({ remote: 'failed', local: 'retained' });
  expect(values.size).toBe(2);
});

it.each(unanswered)('records an unfinished sign-out after %s', async (_name, request) => {
  const values = new Map<string, string>();
  await signedIn(values);
  const marker = memoryMarker();
  await signOutMcp({ profile, store: memoryStore(values), marker, request });
  expect(marker.state.pending).toBe(true);
});

it('clears the grant despite a transient failure when forced', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const request = authorizationServer(() => new Response(null, { status: 503 }));
  expect(
    await signOutMcp({
      profile,
      store: memoryStore(values),
      marker: memoryMarker(),
      request,
      force: true,
    }),
  ).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

const metadataWith = (revocationEndpoint: string) => ({
  issuer: profile.mcpIssuer,
  authorization_endpoint: 'https://issuer.example.test/authorize',
  token_endpoint: 'https://issuer.example.test/token',
  response_types_supported: ['code'],
  revocation_endpoint: revocationEndpoint,
});
it.each([
  [
    'an unsupported token type',
    authorizationServer(() => Response.json({ error: 'unsupported_token_type' }, { status: 400 })),
  ],
  [
    'a deleted client',
    authorizationServer(() => Response.json({ error: 'invalid_client' }, { status: 401 })),
  ],
  ['missing metadata', async () => new Response(null, { status: 404 })],
  [
    'a redirect',
    authorizationServer(() => {
      throw new TypeError('fetch failed', { cause: new Error('unexpected redirect') });
    }),
  ],
  [
    'an insecure revocation endpoint',
    async (url: string | URL | Request) =>
      String(url).includes('.well-known')
        ? Response.json(metadataWith('http://issuer.example.test/revoke'))
        : new Response(null, { status: 200 }),
  ],
] as const)('clears the grant when revocation cannot succeed: %s', async (_name, request) => {
  const values = new Map<string, string>();
  await signedIn(values);
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

it('reports nothing to revoke when no grant is stored', async () => {
  const values = new Map<string, string>();
  const request = authorizationServer(() => new Response(null, { status: 200 }));
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'unavailable',
    local: 'cleared',
  });
});

it('leaves no unfinished sign-out when logout revokes the grant', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const marker = memoryMarker();
  const request = authorizationServer(() => new Response(null, { status: 200 }));
  expect(await signOutMcp({ profile, store: memoryStore(values), marker, request })).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(marker.state.pending).toBe(false);
  expect(values.size).toBe(0);
});

it('records the unfinished sign-out before contacting the authorization server', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const marker = memoryMarker();
  const observed: boolean[] = [];
  const revoked = authorizationServer(() => new Response(null, { status: 200 }));
  const request: typeof fetch = async (url, init) => {
    observed.push(marker.state.pending);
    return revoked(url, init);
  };
  await signOutMcp({ profile, store: memoryStore(values), marker, request });
  expect(observed).toEqual([true, true, true]);
  expect(marker.state.pending).toBe(false);
});

const withoutRevocation: typeof fetch = async (url) =>
  String(url).includes('.well-known')
    ? Response.json({
        ...metadataWith('https://issuer.example.test/revoke'),
        revocation_endpoint: undefined,
      })
    : new Response(null, { status: 200 });
it.each([
  ['revokes the grant', authorizationServer(() => new Response(null, { status: 200 })), 'revoked'],
  ['reports that revocation is unavailable', withoutRevocation, 'unavailable'],
] as const)(
  'finishes an unfinished sign-out when a retried logout %s',
  async (_name, retry, remote) => {
    const values = new Map<string, string>();
    await signedIn(values);
    const marker = memoryMarker();
    const busy = authorizationServer(() => new Response(null, { status: 503 }));
    await signOutMcp({ profile, store: memoryStore(values), marker, request: busy });
    expect(marker.state.pending).toBe(true);
    expect(
      await signOutMcp({ profile, store: memoryStore(values), marker, request: retry }),
    ).toEqual({ remote, local: 'cleared' });
    expect(marker.state.pending).toBe(false);
    expect(values.size).toBe(0);
  },
);

it('finishes an unfinished sign-out offline when forced', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const marker = memoryMarker();
  const offline: typeof fetch = async () => networkFailure('ENOTFOUND')();
  await signOutMcp({ profile, store: memoryStore(values), marker, request: offline });
  expect(marker.state.pending).toBe(true);
  expect(
    await signOutMcp({
      profile,
      store: memoryStore(values),
      marker,
      request: offline,
      force: true,
    }),
  ).toEqual({ remote: 'failed', local: 'cleared' });
  expect(marker.state.pending).toBe(false);
  expect(values.size).toBe(0);
});
