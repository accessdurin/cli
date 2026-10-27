import { cp, lstat, mkdtemp, readdir, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { authorizeMcp, mcpIssuer } from './setup-mcp.js';
import { signInMcp } from './setup.js';
import { cliOptions, CliError } from './cli-options.js';
import { createProfile, FileProfiles } from './profiles.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { FilePendingRevocations, FileRevocationMarker } from './revocation-marker.js';
import { profileConnection } from './profile-client.js';
import { withProfileLock } from './profile-lock.js';
import { toolCommand } from './tool-commands.js';
import { verifyProfile } from './mcp-command.js';
import { logoutCommand } from './profile-commands.js';
import type { RevocationMarker } from './ports/revocation-marker.js';
import type { PendingRevocations, UnrevokedGrant } from './ports/pending-revocations.js';

/** Records the access token each MCP session would present. */
const sessions = vi.hoisted(() => ({ tokens: [] as (string | undefined)[] }));
type TokenSource = { tokens(): Promise<{ access_token: string } | undefined> };
vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    private readonly authorization: TokenSource;
    constructor(options: { authorization: TokenSource }) {
      this.authorization = options.authorization;
    }
    connect = async () => {
      sessions.tokens.push((await this.authorization.tokens())?.access_token);
    };
    close = async () => {};
    listTools = async () => ({ tools: [{ name: 'sandbox__read_document' }] });
  },
}));
/** The OS credential store, shared by sign-in and later commands. */
const keyring = vi.hoisted(() => new Map<string, string>());
vi.mock('./credentials.js', () => ({
  NativeCredentialStore: class {
    read = async (key: string) => keyring.get(key) ?? null;
    write = async (key: string, value: string) => {
      keyring.set(key, value);
    };
    remove = async (key: string) => {
      keyring.delete(key);
    };
  },
}));
afterEach(() => {
  sessions.tokens = [];
  keyring.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

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
const memoryPending = (
  grants: UnrevokedGrant[] = [],
): PendingRevocations & {
  grants: UnrevokedGrant[];
} => ({
  grants,
  entries: async () => [...grants],
  add: async (grant) => {
    grants.push(grant);
  },
  replace: async (remaining) => {
    grants.splice(0, grants.length, ...remaining);
  },
});
const signIn = (marker: RevocationMarker) =>
  authorizeMcp({
    profile,
    store: memoryStore(),
    marker,
    pending: memoryPending(),
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
    pending: memoryPending(),
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
      pending: memoryPending(),
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

  describe('staging', () => {
    const staging = { appOrigin: 'https://moria.app.getdurin.com', development: false };
    const value = `https://moria.mcp.getdurin.com${personal}`;
    const stagingIssuer = 'https://resilient-tiger-64-staging.authkit.app';

    it('accepts the staging MCP gateway and its sign-in server', async () => {
      const { request, requested } = discovery(value, stagingIssuer);
      await expect(mcpIssuer(value, request, staging)).resolves.toBe(stagingIssuer);
      expect(new Set(requested)).toEqual(new Set(['moria.mcp.getdurin.com']));
    });

    it.each([
      ['another staging AuthKit tenant', 'https://evil-tiger-99-staging.authkit.app'],
      ['the bare AuthKit domain', 'https://authkit.app'],
      ['the production sign-in server', 'https://auth.getdurin.com'],
    ])('rejects a staging MCP sign-in server on %s', async (_case, authorizationServer) => {
      const { request } = discovery(value, authorizationServer);
      const failure = mcpIssuer(value, request, staging);
      await expect(failure).rejects.toThrow(new URL(authorizationServer).hostname);
      await expect(failure).rejects.toThrow('resilient-tiger-64-staging.authkit.app');
    });

    it('rejects the staging sign-in server for the production app', async () => {
      const productionEndpoint = `https://mcp.getdurin.com${personal}`;
      const { request } = discovery(productionEndpoint, stagingIssuer);
      const failure = mcpIssuer(productionEndpoint, request, production);
      await expect(failure).rejects.toThrow('resilient-tiger-64-staging.authkit.app');
      await expect(failure).rejects.toThrow('auth.getdurin.com');
    });
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

// MAR-552: signing in after an unfinished logout retires the kept grant; it never refreshes it.
const kept: UnrevokedGrant = {
  clientId: 'kept_client',
  accessToken: 'kept-mcp-access',
  refreshToken: 'kept-mcp-refresh',
};
/** A credential store that still holds the grant a failed logout kept, refresh token included. */
const keptGrantStore = async (values: Map<string, string>) => {
  const provider = new McpOAuthProvider({ profile, store: memoryStore(values) });
  await provider.saveClientInformation({
    client_id: kept.clientId,
    redirect_uris: ['http://127.0.0.1:49152/oauth/callback'],
    issuer,
  });
  await provider.saveTokens({
    access_token: kept.accessToken,
    refresh_token: kept.refreshToken,
    token_type: 'Bearer',
    expires_in: 3600,
    issuer,
  });
  return memoryStore(values);
};
/** The OAuth fixture, recording every request and answering revocation with `revoke`. */
const authorizationServer = (revoke: () => Response) => {
  const requests: { path: string; body: string }[] = [];
  const request: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    requests.push({ path: url.pathname, body: init?.body ? String(init.body) : '' });
    return url.pathname === '/revoke' ? revoke() : oauthFixtureRequest(input, init);
  };
  const sent = (path: string) =>
    requests.filter((entry) => entry.path === path).map(({ body }) => new URLSearchParams(body));
  return { request, requests, sent };
};
/** Later commands reach the authorization server through the global fetch. */
const laterServer = (revoke: () => Response) => {
  const server = authorizationServer(revoke);
  vi.stubGlobal('fetch', server.request);
  return server;
};
const accepted = () => new Response(null, { status: 200 });
const busy = () => new Response(null, { status: 503 });
const revokedTokens = (server: ReturnType<typeof authorizationServer>) =>
  server.sent('/revoke').map((body) => body.get('token'));
const signInAgain = async (input: {
  values: Map<string, string>;
  marker: RevocationMarker;
  pending: PendingRevocations;
  revoke: () => Response;
}) => {
  const server = authorizationServer(input.revoke);
  const opened: string[] = [];
  const tools = await authorizeMcp({
    profile,
    store: await keptGrantStore(input.values),
    marker: input.marker,
    pending: input.pending,
    request: server.request,
    open: async (value) => {
      opened.push(value);
      await fetch(callbackUrl(new URL(value)));
    },
  });
  return { tools, server, opened };
};
const storedTokens = (values: Map<string, string>) =>
  new McpOAuthProvider({ profile, store: memoryStore(values) }).tokens();

it('revokes and deletes the kept grant before signing in again after an unfinished logout', async () => {
  const values = new Map<string, string>();
  const marker = memoryMarker(true);
  const pending = memoryPending();
  const { tools, server } = await signInAgain({ values, marker, pending, revoke: accepted });
  expect(tools).toBe(1);
  expect(
    server
      .sent('/revoke')
      .map((body) => [body.get('token'), body.get('token_type_hint'), body.get('client_id')]),
  ).toEqual([
    [kept.refreshToken, 'refresh_token', kept.clientId],
    [kept.accessToken, 'access_token', kept.clientId],
  ]);
  expect([...values.values()].join()).not.toMatch(/kept/u);
  expect(await storedTokens(values)).toMatchObject({ access_token: 'synthetic-mcp-access' });
  expect(sessions.tokens).toEqual(['synthetic-mcp-access']);
  expect(await marker.pending()).toBe(false);
  expect(pending.grants).toEqual([]);
});

it.each([
  ['a busy server (503)', busy],
  ['a rejected request (400)', () => new Response(null, { status: 400 })],
  [
    'no answer',
    () => {
      throw new TypeError('fetch failed');
    },
  ],
] as const)(
  'hands the kept grant to the pending list and still signs in when revocation meets %s',
  async (_name, revoke) => {
    const values = new Map<string, string>();
    const marker = memoryMarker(true);
    const pending = memoryPending();
    const { tools } = await signInAgain({ values, marker, pending, revoke });
    expect(tools).toBe(1);
    expect(pending.grants).toEqual([kept]);
    expect([...values.values()].join()).not.toMatch(/kept/u);
    expect(await storedTokens(values)).toMatchObject({ access_token: 'synthetic-mcp-access' });
    expect(sessions.tokens).toEqual(['synthetic-mcp-access']);
    expect(await marker.pending()).toBe(false);
  },
);

it.each([
  ['revocation succeeds', accepted],
  ['revocation fails', busy],
] as const)('never reuses the kept grant when signing in again (%s)', async (_name, revoke) => {
  const { server, opened } = await signInAgain({
    values: new Map(),
    marker: memoryMarker(true),
    pending: memoryPending(),
    revoke,
  });
  const firstRevocation = server.requests.findIndex(({ path }) => path === '/revoke');
  expect(firstRevocation).toBeGreaterThanOrEqual(0);
  expect(server.requests.slice(0, firstRevocation).map(({ path }) => path)).not.toContain('/token');
  expect(server.sent('/token').map((body) => body.get('grant_type'))).toEqual([
    'authorization_code',
  ]);
  const otherRequests = server.requests.filter(({ path }) => path !== '/revoke');
  expect(otherRequests.map(({ body }) => body).join()).not.toMatch(/kept/u);
  expect(server.sent('/register')).toHaveLength(1);
  expect(opened).toHaveLength(1);
  expect(sessions.tokens).toEqual(['synthetic-mcp-access']);
});

const withDirectory = async (work: (directory: string) => Promise<void>) => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-pending-revocations-'));
  try {
    await new FileProfiles(directory).save(profile);
    await work(directory);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
};
/** The pending list as durin keeps it: an index on disk, tokens in the OS credential store. */
const pendingList = (directory: string) =>
  new FilePendingRevocations(directory, profile.id, memoryStore(keyring));
/** Signs in as `durin` does after an unfinished logout, with its state on disk. */
const signInWithFiles = async (directory: string, revoke: () => Response) => {
  const marker = new FileRevocationMarker(directory, profile.id);
  await marker.mark();
  return signInAgain({
    values: keyring,
    marker,
    pending: pendingList(directory),
    revoke,
  });
};
const commandOptions = (directory: string, ...command: string[]) =>
  cliOptions([...command, '--config-dir', directory, '--profile', profile.id]);
const silenced = () => [
  vi.spyOn(process.stdout, 'write').mockImplementation(() => true),
  vi.spyOn(process.stderr, 'write').mockImplementation(() => true),
];
const printed = (spies: ReturnType<typeof silenced>) =>
  spies.flatMap((spy) => spy.mock.calls.map(([chunk]) => String(chunk))).join();

it('retries the pending list on later commands, keeping it until revocation succeeds', async () => {
  await withDirectory(async (directory) => {
    const output = silenced();
    await signInWithFiles(directory, busy);
    const pending = pendingList(directory);
    expect(await pending.entries()).toEqual([kept]);
    expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(false);

    const failing = laterServer(busy);
    await toolCommand(commandOptions(directory, 'tools', 'list'));
    expect(revokedTokens(failing)).toContain(kept.refreshToken);
    expect(await pending.entries()).toEqual([kept]);

    const revoking = laterServer(accepted);
    await toolCommand(commandOptions(directory, 'tools', 'list'));
    expect(revokedTokens(revoking)).toEqual([kept.refreshToken, kept.accessToken]);
    expect(await pending.entries()).toEqual([]);

    const settled = laterServer(accepted);
    await toolCommand(commandOptions(directory, 'tools', 'list'));
    expect(settled.sent('/revoke')).toEqual([]);
    expect(sessions.tokens).toEqual(Array(4).fill('synthetic-mcp-access'));
    expect(printed(output)).not.toMatch(/kept-mcp/u);
  });
});

it.each([
  [
    'durin tools list',
    (directory: string) => toolCommand(commandOptions(directory, 'tools', 'list')),
  ],
  ['durin verify', (directory: string) => verifyProfile(commandOptions(directory, 'verify'))],
  [
    'durin mcp serve',
    (directory: string) => profileConnection(commandOptions(directory, 'mcp', 'serve')),
  ],
  ['durin logout', (directory: string) => logoutCommand(commandOptions(directory, 'logout'))],
] as const)('%s retries the pending list', async (_name, command) => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    const server = laterServer(accepted);
    await command(directory);
    expect(revokedTokens(server).slice(0, 2)).toEqual([kept.refreshToken, kept.accessToken]);
    expect(await pendingList(directory).entries()).toEqual([]);
  });
});

it('drops a pending grant once the server says it offers no revocation', async () => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    vi.stubGlobal('fetch', (async (input, init) =>
      new URL(String(input)).pathname.includes('.well-known')
        ? Response.json({ ...metadata(), revocation_endpoint: undefined })
        : oauthFixtureRequest(input, init)) satisfies typeof fetch);
    await profileConnection(commandOptions(directory, 'mcp', 'serve'));
    expect(await pendingList(directory).entries()).toEqual([]);
  });
});

it('skips the pending list without waiting while another process holds the profile lock', async () => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    const server = laterServer(accepted);
    let release = () => {};
    let acquired = () => {};
    const locked = new Promise<void>((resolve) => {
      acquired = resolve;
    });
    const holder = withProfileLock(directory, profile.id, async () => {
      acquired();
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    });
    await locked;
    const started = Date.now();
    await profileConnection(commandOptions(directory, 'mcp', 'serve'));
    expect(Date.now() - started).toBeLessThan(5_000);
    expect(server.sent('/revoke')).toEqual([]);
    release();
    await holder;
    expect(await pendingList(directory).entries()).toEqual([kept]);
  });
});

it('keeps the pending list across a restart, reading only its index on disk and the credential store', async () => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    const restarted = await mkdtemp(join(tmpdir(), 'durin-pending-revocations-restart-'));
    try {
      await cp(directory, restarted, { recursive: true });
      const server = laterServer(accepted);
      await profileConnection(commandOptions(restarted, 'mcp', 'serve'));
      expect(revokedTokens(server)).toEqual([kept.refreshToken, kept.accessToken]);
      expect(await pendingList(restarted).entries()).toEqual([]);
    } finally {
      await rm(restarted, { recursive: true, force: true });
    }
  });
});

it.skipIf(process.platform === 'win32')('keeps the pending list private to this user', async () => {
  await withDirectory(async (directory) => {
    await signInWithFiles(directory, busy);
    const pending = pendingList(directory);
    expect((await stat(pending.path)).mode & 0o777).toBe(0o600);
    expect((await stat(join(directory, 'revocations'))).mode & 0o777).toBe(0o700);
    expect(
      (await readdir(join(directory, 'revocations'))).filter((name) => name.endsWith('.tmp')),
    ).toEqual([]);
  });
});

// MAR-552: pending tokens live in the OS credential store; only a non-secret index is on disk.
const filesUnder = async (directory: string): Promise<string[]> => {
  const names = await readdir(directory, { recursive: true });
  const paths = names.map((name) => join(directory, name));
  const regular = await Promise.all(
    paths.map(async (path) => ((await lstat(path)).isFile() ? path : null)),
  );
  return regular.filter((path): path is string => path !== null);
};
const keptTokenFiles = async (directory: string) => {
  const files = await filesUnder(directory);
  const contents = await Promise.all(files.map((path) => readFile(path, 'utf8')));
  return files.filter((_path, index) => /kept-mcp/u.test(contents[index] ?? ''));
};
const credentialStoreHolds = (token: string) =>
  [...keyring.values()].some((value) => value.includes(token));

it('keeps no token in any file under the durin directory while a revocation is pending', async () => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    expect(await pendingList(directory).entries()).toEqual([kept]);
    expect(await keptTokenFiles(directory)).toEqual([]);
    expect(credentialStoreHolds(kept.refreshToken!)).toBe(true);
    expect(credentialStoreHolds(kept.accessToken)).toBe(true);

    laterServer(accepted);
    await profileConnection(commandOptions(directory, 'mcp', 'serve'));
    expect(await pendingList(directory).entries()).toEqual([]);
    expect(credentialStoreHolds(kept.refreshToken!)).toBe(false);
    expect(await keptTokenFiles(directory)).toEqual([]);
  });
});

const storeUnavailable = () =>
  new CliError(
    'OS credential store unavailable. Unlock your credential store and run durin again.',
  );
it('keeps the block and the kept grant, writing no token to disk, when the credential store refuses the pending grant', async () => {
  await withDirectory(async (directory) => {
    silenced();
    const marker = new FileRevocationMarker(directory, profile.id);
    await marker.mark();
    await keptGrantStore(keyring);
    const store = {
      ...memoryStore(keyring),
      write: async () => {
        throw storeUnavailable();
      },
    };
    const server = authorizationServer(busy);
    await expect(
      authorizeMcp({
        profile,
        store,
        marker,
        pending: new FilePendingRevocations(directory, profile.id, store),
        request: server.request,
        open: async () => {},
      }),
    ).rejects.toThrow(/OS credential store unavailable/u);
    expect(await marker.pending()).toBe(true);
    expect(await storedTokens(keyring)).toMatchObject({ refresh_token: kept.refreshToken });
    expect(await keptTokenFiles(directory)).toEqual([]);
    expect(await pendingList(directory).entries()).toEqual([]);
    expect(server.sent('/register')).toEqual([]);
  });
});

it('keeps the pending list when the credential store cannot be read, without failing the command', async () => {
  await withDirectory(async (directory) => {
    silenced();
    await signInWithFiles(directory, busy);
    const server = laterServer(accepted);
    const pending = new FilePendingRevocations(directory, profile.id, {
      ...memoryStore(keyring),
      read: async () => {
        throw storeUnavailable();
      },
    });
    await expect(pending.entries()).rejects.toThrow(/OS credential store unavailable/u);
    expect(server.sent('/revoke')).toEqual([]);
    expect(await pendingList(directory).entries()).toEqual([kept]);
  });
});

// MAR-552: the setup sign-in retries earlier pending revocations before it signs in.
const seedPending = async (directory: string) => {
  await pendingList(directory).add(kept);
};
it.each([
  ['revoked', accepted, []],
  ['still refused', busy, [kept]],
] as const)(
  'retries the pending list when durin signs in to the profile (%s)',
  async (_name, revoke, remaining) => {
    await withDirectory(async (directory) => {
      await seedPending(directory);
      const server = authorizationServer(revoke);
      const tools = await signInMcp({
        directory,
        profile,
        store: memoryStore(keyring),
        request: server.request,
        open: async (value) => {
          await fetch(callbackUrl(new URL(value)));
        },
      });
      expect(tools).toBe(1);
      const paths = server.requests.map(({ path }) => path);
      expect(paths.indexOf('/revoke')).toBeGreaterThanOrEqual(0);
      expect(paths.indexOf('/revoke')).toBeLessThan(paths.indexOf('/register'));
      expect(server.sent('/revoke')[0]?.get('token')).toBe(kept.refreshToken);
      expect(await pendingList(directory).entries()).toEqual(remaining);
      expect(sessions.tokens).toEqual(['synthetic-mcp-access']);
    });
  },
);

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
  revocation_endpoint: `${issuer}revoke`,
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
