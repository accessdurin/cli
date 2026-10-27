// MAR-552 acceptance: Tess's QA repros of Seth's review findings, folded in unchanged in
// intent. Each test asserts the required behaviour; all of them fail on d8ec8a4.
import { mkdtemp, rm, symlink, mkdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it } from 'vitest';
import { signOutMcp } from './logout.js';
import { createProfile } from './profiles.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { FileRevocationMarker } from './revocation-marker.js';

const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://account.example.test',
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource: `https://app.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://app.example.test',
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
const memoryMarker = () => {
  const state = { pending: false };
  return {
    state,
    pending: async () => state.pending,
    mark: async () => {
      state.pending = true;
    },
    clear: async () => {
      state.pending = false;
    },
  };
};
const signedIn = async (values: Map<string, string>) => {
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
const meta = () =>
  Response.json({
    issuer: profile.mcpIssuer,
    authorization_endpoint: 'https://app.example.test/authorize',
    token_endpoint: 'https://app.example.test/token',
    response_types_supported: ['code'],
    revocation_endpoint: 'https://app.example.test/revoke',
  });
const server =
  (revoke: () => Response | Promise<Response>, metadata: () => Response = meta): typeof fetch =>
  async (url) =>
    String(url).includes('.well-known') ? metadata() : revoke();

// (M) mark() written after network; mark() throwing leaves tokens with no block.
it('SETH-M1a: if writing the marker throws, the kept tokens must not survive unblocked', async () => {
  const values = new Map<string, string>();
  const provider = await signedIn(values);
  const marker = memoryMarker();
  marker.mark = async () => {
    throw Object.assign(new Error('ENOSPC: no space left on device'), { code: 'ENOSPC' });
  };
  await signOutMcp({
    profile,
    store: memoryStore(values),
    marker,
    request: server(() => new Response(null, { status: 503 })),
  }).catch(() => undefined);
  const tokensSurvive = Boolean(await provider.tokens());
  expect(tokensSurvive && !(await marker.pending())).toBe(false);
});

// (M) SIGINT / kill during the (up to 60s) revocation request: marker is only written afterwards.
it('SETH-M1b: while revocation is in flight (process may be killed), the profile is already blocked', async () => {
  const values = new Map<string, string>();
  const provider = await signedIn(values);
  const marker = memoryMarker();
  let arrived = () => {};
  const reached = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  const hanging = server(() => {
    arrived();
    return new Promise<Response>(() => {});
  });
  void signOutMcp({ profile, store: memoryStore(values), marker, request: hanging });
  await reached; // simulate SIGINT here: the process dies with this on-disk/in-store state
  expect(Boolean(await provider.tokens())).toBe(true);
  expect(await marker.pending()).toBe(true);
});

// (L) 401/403/404/400 and 3xx from the revocation endpoint (captive portal / proxy) must count as retry.
it.each([400, 401, 403, 404])(
  'SETH-L1 revocation endpoint answering %i keeps tokens and marks retry',
  async (status) => {
    const values = new Map<string, string>();
    const provider = await signedIn(values);
    const marker = memoryMarker();
    const result = await signOutMcp({
      profile,
      store: memoryStore(values),
      marker,
      request: server(() => new Response('<html>portal</html>', { status })),
    });
    expect(result).toEqual({ remote: 'failed', local: 'retained' });
    expect(Boolean(await provider.tokens())).toBe(true);
    expect(await marker.pending()).toBe(true);
  },
);
it.each([401, 403, 404])(
  'SETH-L1 metadata endpoint answering %i (captive portal) keeps tokens and marks retry',
  async (status) => {
    const values = new Map<string, string>();
    const provider = await signedIn(values);
    const marker = memoryMarker();
    const result = await signOutMcp({
      profile,
      store: memoryStore(values),
      marker,
      request: server(
        () => new Response(null, { status: 200 }),
        () => new Response('<html>portal</html>', { status }),
      ),
    });
    expect(result).toEqual({ remote: 'failed', local: 'retained' });
    expect(await marker.pending()).toBe(true);
    expect(Boolean(await provider.tokens())).toBe(true);
  },
);
it('SETH-L1 revocation endpoint redirecting (302 captive portal) keeps tokens and marks retry', async () => {
  const values = new Map<string, string>();
  const provider = await signedIn(values);
  const marker = memoryMarker();
  const redirect: typeof fetch = async (url) => {
    if (String(url).includes('.well-known')) return meta();
    throw new TypeError('fetch failed', { cause: new Error('unexpected redirect') });
  };
  const result = await signOutMcp({
    profile,
    store: memoryStore(values),
    marker,
    request: redirect,
  });
  expect(result).toEqual({ remote: 'failed', local: 'retained' });
  expect(Boolean(await provider.tokens())).toBe(true);
  expect(await marker.pending()).toBe(true);
});

// (L) revocation-marker.ts:23 uses stat(); a dangling symlink at the marker path reads as "not pending".
it('SETH-L2: a dangling symlink at the marker path still blocks (lstat)', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'qa-marker-symlink-'));
  try {
    const marker = new FileRevocationMarker(directory, profile.id);
    await mkdir(join(directory, 'revocations'), { recursive: true });
    await symlink(join(directory, 'does-not-exist'), marker.path);
    expect(await marker.pending()).toBe(true);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
