import { expect, it } from 'vitest';
import { signOutMcp } from './logout.js';
import { createProfile } from './profiles.js';
import { McpOAuthProvider } from './mcp-oauth.js';

const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://app.example.test/',
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource: `https://app.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://app.example.test/',
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
          authorization_endpoint: 'https://app.example.test/authorize',
          token_endpoint: 'https://app.example.test/token',
          response_types_supported: ['code'],
          revocation_endpoint: 'https://app.example.test/revoke',
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
  authorization_endpoint: 'https://app.example.test/authorize',
  token_endpoint: 'https://app.example.test/token',
  response_types_supported: ['code'],
  revocation_endpoint: revocationEndpoint,
});
it('clears the grant when its revocation endpoint is unsafe, since it can never be used', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const request = async (url: string | URL | Request) =>
    String(url).includes('.well-known')
      ? Response.json(metadataWith('http://app.example.test/revoke'))
      : new Response(null, { status: 200 });
  expect(
    await signOutMcp({ profile, store: memoryStore(values), marker: memoryMarker(), request }),
  ).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

// MAR-552: only a 2xx from the revocation endpoint, or metadata without one, finishes sign-out.
const revocationAnswer = (status: number) =>
  authorizationServer(
    () =>
      new Response(null, {
        status,
        headers: status < 400 ? { location: 'https://portal.example.test/login' } : {},
      }),
  );
const redirectedFetch = () => {
  throw new TypeError('fetch failed', { cause: new Error('unexpected redirect') });
};
const notFoundMetadata: typeof fetch = async (url) =>
  String(url).includes('.well-known')
    ? new Response(null, { status: 404 })
    : new Response(null, { status: 200 });
const withoutRevocationEndpoint: typeof fetch = async (url) =>
  String(url).includes('.well-known')
    ? Response.json({
        ...metadataWith('https://app.example.test/revoke'),
        revocation_endpoint: undefined,
      })
    : new Response(null, { status: 200 });
it.each([
  ['a rejected request (400)', revocationAnswer(400)],
  [
    'an unsupported token type (400)',
    authorizationServer(() => Response.json({ error: 'unsupported_token_type' }, { status: 400 })),
  ],
  ['an unauthenticated client (401)', revocationAnswer(401)],
  ['a forbidden request (403)', revocationAnswer(403)],
  ['a missing endpoint (404)', revocationAnswer(404)],
  ['a captive portal redirect (301)', revocationAnswer(301)],
  ['a captive portal redirect (302)', revocationAnswer(302)],
  ['a captive portal redirect (303)', revocationAnswer(303)],
  ['a captive portal redirect (307)', revocationAnswer(307)],
  ['a captive portal redirect (308)', revocationAnswer(308)],
  ['a refused redirect', authorizationServer(redirectedFetch)],
  ['a refused redirect for metadata', async () => redirectedFetch()],
  ['missing metadata (404)', notFoundMetadata],
  [
    'metadata behind a captive portal (302)',
    async () =>
      new Response(null, {
        status: 302,
        headers: { location: 'https://portal.example.test/login' },
      }),
  ],
] as const)(
  'keeps the grant and records an unfinished sign-out after %s',
  async (_name, request) => {
    const values = new Map<string, string>();
    await signedIn(values);
    const marker = memoryMarker();
    expect(await signOutMcp({ profile, store: memoryStore(values), marker, request })).toEqual({
      remote: 'failed',
      local: 'retained',
    });
    expect(values.size).toBe(2);
    expect(marker.state.pending).toBe(true);
  },
);

it('finishes sign-out only when the server’s metadata says it offers no revocation', async () => {
  const values = new Map<string, string>();
  await signedIn(values);
  const marker = memoryMarker();
  expect(
    await signOutMcp({
      profile,
      store: memoryStore(values),
      marker,
      request: withoutRevocationEndpoint,
    }),
  ).toEqual({ remote: 'unavailable', local: 'cleared' });
  expect(values.size).toBe(0);
  expect(marker.state.pending).toBe(false);
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
        ...metadataWith('https://app.example.test/revoke'),
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
