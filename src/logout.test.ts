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
  expect(await signOutMcp({ profile, store: memoryStore(values), request })).toEqual({
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
  expect(await signOutMcp({ profile, store: memoryStore(values), request })).toEqual({
    remote: 'failed',
    local: 'retained',
  });
  expect(await provider.tokens()).toMatchObject({ refresh_token: 'synthetic-refresh' });
  status = 200;
  expect(await signOutMcp({ profile, store: memoryStore(values), request })).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

it('clears saved credentials that cannot be read, since they cannot be revoked', async () => {
  const values = new Map<string, string>([[`mcp:${profile.id}:tokens`, 'not json']]);
  const request = authorizationServer(() => new Response(null, { status: 500 }));
  expect(await signOutMcp({ profile, store: memoryStore(values), request })).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});

it.each([
  ['a busy server', () => new Response(null, { status: 503 })],
  ['rate limiting', () => new Response(null, { status: 429 })],
  [
    'a network failure',
    () => {
      throw new TypeError('fetch failed');
    },
  ],
])('keeps the grant after %s', async (_name, revoke) => {
  const values = new Map<string, string>();
  await signedIn(values);
  expect(
    await signOutMcp({ profile, store: memoryStore(values), request: authorizationServer(revoke) }),
  ).toEqual({ remote: 'failed', local: 'retained' });
  expect(values.size).toBe(2);
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
    'an insecure revocation endpoint',
    async (url: string | URL | Request) =>
      String(url).includes('.well-known')
        ? Response.json(metadataWith('http://issuer.example.test/revoke'))
        : new Response(null, { status: 200 }),
  ],
] as const)('clears the grant when revocation cannot succeed: %s', async (_name, request) => {
  const values = new Map<string, string>();
  await signedIn(values);
  expect(await signOutMcp({ profile, store: memoryStore(values), request })).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(values.size).toBe(0);
});
