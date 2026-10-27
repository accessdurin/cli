import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import { authorizeMcp, mcpIssuer } from './setup-mcp.js';
import { createProfile } from './profiles.js';
import { FileRevocationMarker } from './revocation-marker.js';
import type { RevocationMarker } from './ports/revocation-marker.js';

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
const memoryStore = (values = new Map<string, string>()) => ({
  read: async (key: string) => values.get(key) ?? null,
  write: async (key: string, value: string) => {
    values.set(key, value);
  },
  remove: async (key: string) => {
    values.delete(key);
  },
});
const signIn = (marker: RevocationMarker) =>
  authorizeMcp({
    profile,
    store: memoryStore(),
    marker,
    request: oauthFixtureRequest,
    open: async (value) => {
      await fetch(callbackUrl(new URL(value)));
    },
  });

it('finishes an unfinished sign-out when this profile signs in again', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-revocation-sign-in-'));
  try {
    await new FileRevocationMarker(directory, profile.id).mark();
    expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(true);
    expect(await signIn(new FileRevocationMarker(directory, profile.id))).toBe(1);
    expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(false);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

it('clears an unfinished sign-out once the new sign-in is authorized', async () => {
  const marker = memoryMarker(true);
  const cleared = vi.spyOn(marker, 'clear');
  expect(await signIn(marker)).toBe(1);
  expect(cleared).toHaveBeenCalledOnce();
  expect(await marker.pending()).toBe(false);
});

it('authorizes with the exact personal MCP resource without injecting Durin scopes', async () => {
  const redirects: URL[] = [];
  const values = new Map<string, string>();
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
  const tools = await authorizeMcp({
    profile,
    store,
    marker: memoryMarker(),
    request: oauthFixtureRequest,
    open: async (value) => {
      const authorization = new URL(value);
      redirects.push(authorization);
      await fetch(callbackUrl(authorization));
    },
  });
  expect(tools).toBe(1);
  expect(redirects[0]?.searchParams.get('resource')).toBe(resource);
  expect(redirects[0]?.searchParams.get('scope')).toBeNull();
});

it.each([
  'file:///C:/Windows/System32/calc.exe',
  'file://attacker.example.test/share/payload.exe',
  'smb://attacker.example.test/share',
  'ms-msdt:/id%20PCWDiagnostic',
  'http://attacker.example.test/authorize',
])('never opens a non-HTTPS authorization endpoint: %s', async (endpoint) => {
  const opened: string[] = [];
  const marker = memoryMarker(true);
  const values = new Map<string, string>();
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
  await expect(
    authorizeMcp({
      profile,
      store,
      marker,
      request: (input, init) =>
        new URL(String(input)).pathname.includes('oauth-authorization-server')
          ? Promise.resolve(
              Response.json({ ...metadata(), authorization_endpoint: endpoint }, { status: 200 }),
            )
          : oauthFixtureRequest(input, init),
      open: async (value) => {
        opened.push(value);
      },
    }),
  ).rejects.toThrow('Unsafe MCP authorization URL.');
  expect(opened).toEqual([]);
  expect(await marker.pending()).toBe(true);
});

describe('MCP issuer discovery host pinning', () => {
  const personal = `/mcp/u/${'a'.repeat(64)}`;
  const production = { appOrigin: 'https://app.getdurin.com', development: false };
  const discovery = (resourceUrl: string, authorizationServer: string) => {
    const requested: string[] = [];
    const request: typeof fetch = async (input) => {
      const url = new URL(String(input));
      requested.push(url.host);
      return url.pathname.includes('oauth-protected-resource')
        ? Response.json({ resource: resourceUrl, authorization_servers: [authorizationServer] })
        : new Response(null, { status: 404 });
    };
    return { request, requested };
  };

  it('accepts the production MCP gateway and its sign-in server', async () => {
    const value = `https://mcp.getdurin.com${personal}`;
    const { request } = discovery(value, 'https://auth.getdurin.com');
    await expect(mcpIssuer(value, request, production)).resolves.toBe('https://auth.getdurin.com');
  });

  it.each([
    ['a lookalike host', 'https://evil-durin.com', 'evil-durin.com'],
    ['a suffix lookalike host', 'https://getdurin.com.evil.test', 'getdurin.com.evil.test'],
    ['a subdomain that is not allowed', 'https://evil.getdurin.com', 'evil.getdurin.com'],
    ['a loopback host outside development', 'https://127.0.0.1:8443', '127.0.0.1'],
  ])(
    'rejects a personal MCP resource on %s before contacting it',
    async (_case, origin, unexpected) => {
      const value = `${origin}${personal}`;
      const { request, requested } = discovery(value, 'https://auth.getdurin.com');
      const failure = mcpIssuer(value, request, production);
      await expect(failure).rejects.toThrow(unexpected);
      await expect(failure).rejects.toThrow('mcp.getdurin.com');
      expect(requested).toEqual([]);
    },
  );

  it.each([
    ['a lookalike host', 'https://evil-durin.com', 'evil-durin.com'],
    [
      'a suffix lookalike host',
      'https://auth.getdurin.com.evil.test',
      'auth.getdurin.com.evil.test',
    ],
    ['a subdomain that is not allowed', 'https://login.getdurin.com', 'login.getdurin.com'],
    ['the MCP gateway host', 'https://mcp.getdurin.com', 'mcp.getdurin.com'],
    ['a loopback host outside development', 'https://localhost', 'localhost'],
  ])('rejects an MCP sign-in server on %s', async (_case, authorizationServer, unexpected) => {
    const value = `https://mcp.getdurin.com${personal}`;
    const { request } = discovery(value, authorizationServer);
    const failure = mcpIssuer(value, request, production);
    await expect(failure).rejects.toThrow(unexpected);
    await expect(failure).rejects.toThrow('auth.getdurin.com');
  });

  it('accepts a loopback MCP gateway and sign-in server in development', async () => {
    const value = `http://127.0.0.1:5272${personal}`;
    const { request } = discovery(value, 'http://127.0.0.1:5272/');
    await expect(
      mcpIssuer(value, request, { appOrigin: 'http://127.0.0.1:5270', development: true }),
    ).resolves.toBe('http://127.0.0.1:5272/');
  });

  it('rejects a loopback MCP gateway when development is off, even on the app host', async () => {
    const value = `https://127.0.0.1:8443${personal}`;
    const { request, requested } = discovery(value, 'https://127.0.0.1:8443/');
    await expect(
      mcpIssuer(value, request, { appOrigin: 'https://127.0.0.1:8443', development: false }),
    ).rejects.toThrow('127.0.0.1');
    expect(requested).toEqual([]);
  });
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
  response_types_supported: ['code'],
  code_challenge_methods_supported: ['S256'],
  token_endpoint_auth_methods_supported: ['none'],
  authorization_response_iss_parameter_supported: true,
});

const tokens = () => ({
  access_token: 'synthetic-mcp-access',
  refresh_token: 'synthetic-mcp-refresh',
  token_type: 'Bearer',
  expires_in: 3600,
});

const callbackUrl = (authorization: URL): string => {
  const callback = new URL(authorization.searchParams.get('redirect_uri')!);
  callback.search = new URLSearchParams({
    code: 'synthetic-code',
    state: authorization.searchParams.get('state')!,
    iss: issuer,
  }).toString();
  return callback.href;
};
