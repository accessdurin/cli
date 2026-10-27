// MAR-552: a logout that could not revoke the grant blocks the profile until sign-out finishes.
import { chmod, cp, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { PassThrough } from 'node:stream';
import { setTimeout } from 'node:timers/promises';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cliOptions, CliError } from './cli-options.js';
import { signOutMcp } from './logout.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { profileConnection } from './profile-client.js';
import { withProfileLock } from './profile-lock.js';
import { createProfile, FileProfiles } from './profiles.js';
import { FileRevocationMarker } from './revocation-marker.js';
import { toolCommand } from './tool-commands.js';

const remote = vi.hoisted(() => ({ connect: vi.fn<() => Promise<void>>(async () => {}) }));
vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = remote.connect;
    close = async () => {};
    listTools = async () => ({ tools: [] });
  },
}));

const scope = {
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
} as const;
const profile = createProfile(scope);
const otherProfile = createProfile({ ...scope, principalId: 'user_two' });
const pendingMessage = /durin logout again[\s\S]*durin logout --force/u;

let directory = '';
beforeEach(async () => {
  directory = await mkdtemp(join(tmpdir(), 'durin-revocation-'));
  const profiles = new FileProfiles(directory);
  await profiles.save(profile);
  await profiles.save(otherProfile);
  await profiles.activate(profile.id);
  remote.connect.mockClear();
  vi.spyOn(process.stdout, 'write').mockImplementation(() => true);
});
afterEach(async () => {
  vi.restoreAllMocks();
  await rm(directory, { recursive: true, force: true });
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
const signedInStore = async () => {
  const values = new Map<string, string>();
  const provider = new McpOAuthProvider({ profile, store: memoryStore(values) });
  await provider.saveClientInformation({ client_id: 'client_one', issuer: profile.mcpIssuer });
  await provider.saveTokens({
    access_token: 'synthetic-access',
    refresh_token: 'synthetic-refresh',
    token_type: 'Bearer',
    issuer: profile.mcpIssuer,
  });
  return memoryStore(values);
};
const authorizationServer =
  (revoke: () => Response): typeof fetch =>
  async (url) =>
    String(url).includes('.well-known')
      ? Response.json({
          issuer: profile.mcpIssuer,
          authorization_endpoint: 'https://issuer.example.test/authorize',
          token_endpoint: 'https://issuer.example.test/token',
          response_types_supported: ['code'],
          revocation_endpoint: 'https://issuer.example.test/revoke',
        })
      : revoke();
const networkFailure = (code: string) => () => {
  throw new TypeError('fetch failed', { cause: Object.assign(new Error(code), { code }) });
};
const unreachable =
  (failure: () => never): typeof fetch =>
  async () =>
    failure();
const revoked = authorizationServer(() => new Response(null, { status: 200 }));
const busy = authorizationServer(() => new Response(null, { status: 503 }));
const offline = unreachable(networkFailure('ENOTFOUND'));

/** Logs out as `durin logout` does: under the profile lock, with markers read from disk. */
const logout = async (options: {
  store: ReturnType<typeof memoryStore>;
  request: typeof fetch;
  force?: boolean;
}) =>
  withProfileLock(directory, profile.id, () =>
    signOutMcp({ profile, marker: new FileRevocationMarker(directory, profile.id), ...options }),
  );

const tools = (selected = profile.id) =>
  toolCommand(cliOptions(['tools', 'list', '--config-dir', directory, '--profile', selected]));

/** A fresh `durin mcp serve` process for the active profile, with a recorded MCP server. */
const startBridge = async () => {
  const input = new PassThrough();
  const output = new PassThrough();
  const forwarded: unknown[] = [];
  const connection = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  const bridge = new McpStdioBridge({
    ...connection,
    authorization: { token: async () => 'synthetic-access' },
    fetch: async (_url, init) => {
      const message = JSON.parse(String(init?.body)) as { id: number };
      forwarded.push(message);
      return Response.json({ jsonrpc: '2.0', id: message.id, result: { content: [] } });
    },
    input,
    output,
  });
  await bridge.start();
  const lines = output[Symbol.asyncIterator]();
  const call = async (id: number) => {
    input.write(
      `${JSON.stringify({ jsonrpc: '2.0', id, method: 'tools/call', params: { name: 'sandbox__read_document', arguments: {} } })}\n`,
    );
    const { value } = await lines.next();
    return JSON.parse(String(value)) as {
      id: number;
      result?: unknown;
      error?: { message: string };
    };
  };
  return { call, forwarded, close: () => bridge.close() };
};
const expectBlocked = async () => {
  const bridge = await startBridge();
  try {
    const answer = await bridge.call(1);
    expect(answer).toMatchObject({ error: { message: expect.stringMatching(pendingMessage) } });
    expect(bridge.forwarded).toEqual([]);
  } finally {
    await bridge.close();
  }
  const refused = await tools().catch((error: unknown) => error);
  expect(refused).toBeInstanceOf(CliError);
  expect((refused as Error).message).toMatch(pendingMessage);
  expect(remote.connect).not.toHaveBeenCalled();
};
const expectUnblocked = async () => {
  const bridge = await startBridge();
  try {
    expect((await bridge.call(1)).result).toEqual({ content: [] });
    expect(bridge.forwarded).toHaveLength(1);
  } finally {
    await bridge.close();
  }
  await tools();
  expect(remote.connect).toHaveBeenCalledOnce();
};

it.each([
  [
    'no answer before the timeout',
    unreachable(() => {
      throw new DOMException('The operation was aborted due to timeout', 'TimeoutError');
    }),
  ],
  ['rate limiting (429)', authorizationServer(() => new Response(null, { status: 429 }))],
  ['a server error (5xx)', busy],
  ['a DNS failure (ENOTFOUND)', offline],
  ['a reset connection (ECONNRESET)', unreachable(networkFailure('ECONNRESET'))],
  [
    'an unparseable answer',
    async () =>
      new Response('<html>Sign in to Wi-Fi</html>', {
        status: 200,
        headers: { 'content-type': 'text/html' },
      }),
  ],
] as const)(
  'blocks the MCP bridge and durin tools after a logout that failed with %s',
  async (_name, request) => {
    expect(await logout({ store: await signedInStore(), request })).toEqual({
      remote: 'failed',
      local: 'retained',
    });
    expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(true);
    await expectBlocked();
  },
);

it('unblocks the profile once a retried logout revokes the grant', async () => {
  const store = await signedInStore();
  await logout({ store, request: busy });
  await expectBlocked();
  expect(await logout({ store, request: revoked })).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(false);
  await expectUnblocked();
});

it('unblocks the profile when logout --force runs offline', async () => {
  const store = await signedInStore();
  await logout({ store, request: offline });
  await expectBlocked();
  expect(await logout({ store, request: offline, force: true })).toEqual({
    remote: 'failed',
    local: 'cleared',
  });
  expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(false);
  await expectUnblocked();
});

it('leaves the bridge and durin tools working after a successful logout', async () => {
  expect(await logout({ store: await signedInStore(), request: revoked })).toEqual({
    remote: 'revoked',
    local: 'cleared',
  });
  expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(false);
  await expectUnblocked();
});

it('keeps the block across a restart, reading only what was saved on disk', async () => {
  await logout({ store: await signedInStore(), request: busy });
  const restarted = await mkdtemp(join(tmpdir(), 'durin-revocation-restart-'));
  try {
    await cp(directory, restarted, { recursive: true });
    expect(await new FileRevocationMarker(restarted, profile.id).pending()).toBe(true);
  } finally {
    await rm(restarted, { recursive: true, force: true });
  }
});

it('blocks only the profile whose sign-out is unfinished', async () => {
  await new FileRevocationMarker(directory, profile.id).mark();
  expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(true);
  expect(await new FileRevocationMarker(directory, otherProfile.id).pending()).toBe(false);
  await tools(otherProfile.id);
  expect(remote.connect).toHaveBeenCalledOnce();
});

it('blocks a bridge that was already running when another process failed to log out', async () => {
  const bridge = await startBridge();
  try {
    expect((await bridge.call(1)).result).toEqual({ content: [] });
    await logout({ store: await signedInStore(), request: busy });
    expect(await bridge.call(2)).toMatchObject({
      error: { message: expect.stringMatching(pendingMessage) },
    });
    expect(bridge.forwarded).toHaveLength(1);
  } finally {
    await bridge.close();
  }
});

it('refuses a bridge call that waited for the profile lock while logout failed', async () => {
  const bridge = await startBridge();
  let release = (_response: Response) => {};
  let arrived = () => {};
  const reached = new Promise<void>((resolve) => {
    arrived = resolve;
  });
  const answer = new Promise<Response>((resolve) => {
    release = resolve;
  });
  const slowServer = authorizationServer(() => new Response(null, { status: 503 }));
  const request: typeof fetch = async (url, init) => {
    if (String(url).includes('.well-known')) return slowServer(url, init);
    arrived();
    return answer;
  };
  try {
    const signingOut = logout({ store: await signedInStore(), request });
    await reached;
    const waiting = bridge.call(1);
    await setTimeout(150);
    expect(bridge.forwarded).toEqual([]);
    release(new Response(null, { status: 503 }));
    expect(await signingOut).toEqual({ remote: 'failed', local: 'retained' });
    expect(await waiting).toMatchObject({
      error: { message: expect.stringMatching(pendingMessage) },
    });
    expect(bridge.forwarded).toEqual([]);
  } finally {
    await bridge.close();
  }
});

it('records one unfinished sign-out when two processes mark and read it at once', async () => {
  const first = new FileRevocationMarker(directory, profile.id);
  const second = new FileRevocationMarker(directory, profile.id);
  await Promise.all([first.mark(), second.mark(), first.mark()]);
  expect(await Promise.all([first.pending(), second.pending()])).toEqual([true, true]);
  await Promise.all([second.clear(), second.clear()]);
  expect(await first.pending()).toBe(false);
});

it('treats a damaged marker as an unfinished sign-out', async () => {
  const marker = new FileRevocationMarker(directory, profile.id);
  await marker.mark();
  await writeFile(marker.path, '\u0000not a marker');
  expect(await marker.pending()).toBe(true);
  await expect(tools()).rejects.toThrow(pendingMessage);
});

it.skipIf(process.platform === 'win32' || process.getuid?.() === 0)(
  'treats a marker that cannot be read as an unfinished sign-out',
  async () => {
    const marker = new FileRevocationMarker(directory, profile.id);
    await marker.mark();
    await chmod(dirname(marker.path), 0o000);
    try {
      expect(await marker.pending()).toBe(true);
      await expect(tools()).rejects.toThrow(pendingMessage);
    } finally {
      await chmod(dirname(marker.path), 0o700);
    }
  },
);
