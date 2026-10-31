// MAR-552 acceptance: Tess's QA repros of a sign-in after a failed logout (from
// qa-seth-cli7-signin.test.ts and qa-signin-reuse.test.ts). The "CURRENT BEHAVIOUR (repro)" test,
// identical in both, is folded in once and inverted to the required behaviour.
import { expect, it, vi } from 'vitest';
import { authorizeMcp } from './setup-mcp.js';
import { createProfile } from './profiles.js';
import type { UnrevokedGrant } from './ports/pending-revocations.js';

vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = async () => {};
    close = async () => {};
    listTools = async () => ({ tools: [{ name: 'sandbox__read_document' }] });
  },
}));

const resource = `https://app.example.test/mcp/u/${'a'.repeat(64)}`;
const issuer = 'https://app.example.test/';
const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: issuer,
  accountClientId: 'account_client',
  principalId: 'user_one',
  workosOrganizationId: 'org_provider',
  organizationId: 'org_one',
  environment: 'production',
  resource,
  mcpIssuer: issuer,
  development: false,
});

const memoryMarker = (initial = false) => {
  let pending = initial;
  return {
    pending: async () => pending,
    mark: async () => {
      pending = true;
    },
    clear: async () => {
      pending = false;
    },
  };
};
const run = async (values: Map<string, string>, marker: ReturnType<typeof memoryMarker>) => {
  const seen: string[] = [];
  const opened: string[] = [];
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
  const grants: UnrevokedGrant[] = [];
  const pending = {
    entries: async () => [...grants],
    add: async (grant: UnrevokedGrant) => {
      grants.push(grant);
    },
    replace: async (remaining: readonly UnrevokedGrant[]) => {
      grants.splice(0, grants.length, ...remaining);
    },
  };
  const tools = await authorizeMcp({
    profile,
    store,
    marker,
    pending,
    request: (input, init) => {
      seen.push(`${init?.method ?? 'GET'} ${new URL(String(input)).pathname}`);
      return oauthFixtureRequest(input, init);
    },
    open: async (value) => {
      opened.push(value);
      await fetch(callbackUrl(new URL(value)));
    },
  });
  return { tools, seen, opened };
};
const tokenKey = (values: Map<string, string>) =>
  [...values.keys()].find((k) => k.includes('tokens'))!;

it('re-sign-in with kept tokens revokes the old grant before it clears the block', async () => {
  const values = new Map<string, string>();
  await run(values, memoryMarker()); // first sign-in stores tokens
  const marker = memoryMarker(true); // failed logout: tokens retained + marker set
  const second = await run(values, marker);
  expect(await marker.pending()).toBe(false); // block lifted
  expect(second.seen.some((r) => /revoke/i.test(r))).toBe(true); // old grant revoked
});

it('SETH-H: sign-in with marker + stored refresh token revokes and deletes the kept grant BEFORE auth() (no silent refresh)', async () => {
  const values = new Map<string, string>();
  await run(values, memoryMarker());
  const kept = values.get(tokenKey(values));
  const second = await run(values, memoryMarker(true));
  const revokeAt = second.seen.findIndex((r) => /revoke/i.test(r));
  const refreshAt = second.seen.findIndex((r) => r === 'POST /token');
  console.log('QA SETH-H requests:', JSON.stringify(second.seen));
  expect(revokeAt).toBeGreaterThanOrEqual(0);
  expect(refreshAt === -1 || revokeAt < refreshAt).toBe(true);
  expect(second.opened.length).toBe(1); // fresh browser sign-in, kept tokens never reused
  expect(values.get(tokenKey(values))).not.toBe(kept);
});

it('SETH REQUIRED: re-sign-in with marker revokes kept tokens and never reuses them', async () => {
  const values = new Map<string, string>();
  await run(values, memoryMarker());
  const kept = values.get(tokenKey(values));
  const second = await run(values, memoryMarker(true));
  expect(second.seen.some((r) => /revoke/i.test(r))).toBe(true);
  expect(second.opened.length).toBe(1);
  expect(values.get(tokenKey(values))).not.toBe(kept);
});

const oauthFixtureRequest: typeof fetch = async (input, init) => {
  const url = new URL(String(input));
  const route = oauthRoutes(init).find(({ match }) => match(url));
  return route?.response() ?? Response.json(tokens());
};

const oauthRoutes = (init?: RequestInit) => [
  {
    match: (url: URL) => url.pathname.includes('oauth-protected-resource'),
    response: () => Response.json({ resource, authorization_servers: [issuer] }),
  },
  {
    match: (url: URL) => url.pathname.includes('.well-known'),
    response: () => Response.json(metadata()),
  },
  {
    match: (url: URL) => url.pathname === '/register',
    response: () =>
      Response.json({ ...JSON.parse(String(init?.body)), client_id: 'mcp_public_client' }),
  },
];

const metadata = () => ({
  issuer,
  scopes_supported: ['email'],
  authorization_endpoint: `${issuer}authorize`,
  token_endpoint: `${issuer}token`,
  registration_endpoint: `${issuer}register`,
  // Revocation needs an advertised endpoint; the QA fixture predates it.
  revocation_endpoint: `${issuer}revoke`,
  response_types_supported: ['code'],
  code_challenge_methods_supported: ['S256'],
  token_endpoint_auth_methods_supported: ['none'],
  authorization_response_iss_parameter_supported: true,
});

/** Each issuance is distinct, so a replaced grant can be told apart from a reused one. */
let issued = 0;
const tokens = () => {
  issued += 1;
  return {
    access_token: `synthetic-mcp-access-${issued}`,
    refresh_token: `synthetic-mcp-refresh-${issued}`,
    token_type: 'Bearer',
    expires_in: 3600,
  };
};

const callbackUrl = (authorization: URL): string => {
  const callback = new URL(authorization.searchParams.get('redirect_uri')!);
  callback.search = new URLSearchParams({
    code: 'synthetic-code',
    state: authorization.searchParams.get('state')!,
    iss: issuer,
  }).toString();
  return callback.href;
};
