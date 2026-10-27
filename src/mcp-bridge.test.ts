// MAR-552: a bridge that was already open stops using the grant once sign-out is unfinished.
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PassThrough } from 'node:stream';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { auth, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import { cliOptions, CliError } from './cli-options.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { profileConnection } from './profile-client.js';
import { createProfile, FileProfiles } from './profiles.js';
import { FileRevocationMarker } from './revocation-marker.js';
import { toolCommand } from './tool-commands.js';

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

const issuer = 'https://app.example.test/';
const resource = `https://app.example.test/mcp/u/${'a'.repeat(64)}`;
const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://account.example.test',
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource,
  mcpIssuer: issuer,
  development: false,
});
const pendingMessage = /durin logout again[\s\S]*durin logout --force/u;

let directory = '';
beforeEach(async () => {
  directory = await mkdtemp(join(tmpdir(), 'durin-open-bridge-'));
  const profiles = new FileProfiles(directory);
  await profiles.save(profile);
  await profiles.activate(profile.id);
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
  const provider = new McpOAuthProvider({ profile, store });
  await provider.saveClientInformation({ client_id: 'client_one', issuer });
  await provider.saveTokens({
    access_token: 'old-access',
    refresh_token: 'old-refresh',
    token_type: 'Bearer',
    issuer,
  });
  for (const [key, value] of values) keyring.set(key, value);
});
afterEach(async () => {
  keyring.clear();
  await rm(directory, { recursive: true, force: true });
});

const markedByAnotherProcess = () => new FileRevocationMarker(directory, profile.id).mark();

/** A server event stream that the test feeds, and that records whether the client hung up. */
const eventStream = (signal: AbortSignal | null | undefined) => {
  let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
  const body = new ReadableStream<Uint8Array>({
    start: (value) => {
      controller = value;
    },
  });
  signal?.addEventListener('abort', () => controller?.error(signal.reason));
  const encoder = new TextEncoder();
  return {
    body,
    signal,
    push: (message: unknown) =>
      controller?.enqueue(encoder.encode(`data: ${JSON.stringify(message)}\n\n`)),
  };
};
type Recorded = { method: string; path: string; authorization: string | null; body: string };
const recorded = (input: string | URL | Request, init: RequestInit = {}): Recorded => ({
  method: init.method ?? 'GET',
  path: new URL(String(input)).pathname,
  authorization: new Headers(init.headers).get('authorization'),
  body: String(init.body ?? ''),
});
const authorizationRoutes = [
  {
    match: (path: string) => path.startsWith('/.well-known/oauth-protected-resource'),
    response: () => Response.json({ resource, authorization_servers: [issuer] }),
  },
  {
    match: (path: string) => path.startsWith('/.well-known/'),
    response: () =>
      Response.json({
        issuer,
        authorization_endpoint: `${issuer}authorize`,
        token_endpoint: `${issuer}token`,
        response_types_supported: ['code'],
        code_challenge_methods_supported: ['S256'],
      }),
  },
  {
    match: (path: string) => path === '/token',
    response: () => Response.json({ access_token: 'refreshed-access', token_type: 'Bearer' }),
  },
];
const mcpResult = (method: string) =>
  method === 'initialize'
    ? {
        protocolVersion: '2025-06-18',
        capabilities: {},
        serverInfo: { name: 'durin', version: '1.0.0' },
      }
    : { content: [] };
const mcpAnswer = (body: string) => {
  const message = JSON.parse(body) as { id?: number; method: string };
  if (message.id === undefined) return new Response(null, { status: 202 });
  return Response.json(
    { jsonrpc: '2.0', id: message.id, result: mcpResult(message.method) },
    { headers: { 'mcp-session-id': 'session-one' } },
  );
};
/** Once the server revokes the grant, MCP requests and refreshes are refused (MAR-534 case 8). */
const revokedRoutes = [
  {
    match: (path: string) => path === '/token',
    response: () =>
      Response.json(
        { error: 'invalid_grant', error_description: 'The grant was revoked.' },
        { status: 400 },
      ),
  },
  {
    match: (path: string) => path.startsWith('/mcp/'),
    response: () =>
      new Response(null, {
        status: 401,
        headers: { 'www-authenticate': 'Bearer error="invalid_token"' },
      }),
  },
];
/** The organization's MCP server and authorization server, recording every request. */
const durinServer = () => {
  const requests: Recorded[] = [];
  const streams: ReturnType<typeof eventStream>[] = [];
  let routes = authorizationRoutes;
  const revoke = () => {
    routes = [...revokedRoutes, ...authorizationRoutes];
  };
  const openStream = (signal: AbortSignal | null | undefined) => {
    const stream = eventStream(signal);
    streams.push(stream);
    return new Response(stream.body, { headers: { 'content-type': 'text/event-stream' } });
  };
  const request: typeof fetch = async (input, init) => {
    const entry = recorded(input, init);
    requests.push(entry);
    const route = routes.find(({ match }) => match(entry.path));
    if (route) return route.response();
    return entry.method === 'GET' ? openStream(init?.signal) : mcpAnswer(entry.body);
  };
  return { request, requests, streams, revoke };
};
const usesOldGrant = ({ authorization, body }: Recorded) =>
  authorization === 'Bearer old-access' || body.includes('old-refresh');

/** `durin mcp serve` for the active profile, connected and with its event stream open. */
const openBridge = async (server: ReturnType<typeof durinServer>) => {
  const input = new PassThrough();
  const output = new PassThrough();
  const errors: string[] = [];
  const connection = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  const bridge = new McpStdioBridge({
    ...connection,
    fetch: server.request,
    input,
    output,
    onError: (message) => errors.push(message),
  });
  await bridge.start();
  const lines = output[Symbol.asyncIterator]();
  const received: unknown[] = [];
  const next = async () => {
    const { value } = await lines.next();
    const message: unknown = JSON.parse(String(value));
    received.push(message);
    return message;
  };
  const write = (message: unknown) => input.write(`${JSON.stringify(message)}\n`);
  write({
    jsonrpc: '2.0',
    id: 1,
    method: 'initialize',
    params: {
      protocolVersion: '2025-06-18',
      capabilities: {},
      clientInfo: { name: 'agent', version: '1.0.0' },
    },
  });
  await next();
  write({ jsonrpc: '2.0', method: 'notifications/initialized' });
  await vi.waitFor(() => expect(server.streams).toHaveLength(1));
  return { bridge, next, write, received, errors, stream: server.streams[0]! };
};

it('stops delivering an open bridge’s event stream once another process marks sign-out unfinished', async () => {
  const server = durinServer();
  const open = await openBridge(server);
  try {
    open.stream.push({ jsonrpc: '2.0', method: 'notifications/tools/list_changed' });
    expect(await open.next()).toMatchObject({ method: 'notifications/tools/list_changed' });
    await markedByAnotherProcess();
    const sent = server.requests.length;
    open.stream.push({ jsonrpc: '2.0', method: 'notifications/resources/list_changed' });
    await vi.waitFor(() => expect(open.stream.signal?.aborted).toBe(true), { timeout: 3_000 });
    expect(open.received).toHaveLength(2);
    expect(open.errors.join('\n')).toMatch(pendingMessage);
    expect(server.requests.slice(sent).filter(usesOldGrant)).toEqual([]);
  } finally {
    await open.bridge.close();
  }
});

it('closes an open bridge’s event stream once sign-out is unfinished, even when it is quiet', async () => {
  const server = durinServer();
  const open = await openBridge(server);
  try {
    await markedByAnotherProcess();
    const sent = server.requests.length;
    await vi.waitFor(() => expect(open.stream.signal?.aborted).toBe(true), { timeout: 3_000 });
    expect(open.errors.join('\n')).toMatch(pendingMessage);
    expect(server.requests.slice(sent).filter(usesOldGrant)).toEqual([]);
  } finally {
    await open.bridge.close();
  }
});

it('hands out no token from the profile connection once sign-out is unfinished', async () => {
  const { authorization } = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  const provider = authorization as McpOAuthProvider;
  expect(await provider.tokens()).toMatchObject({ access_token: 'old-access' });
  await markedByAnotherProcess();
  await expect(provider.tokens()).rejects.toThrow(pendingMessage);
});

it('refuses to refresh the grant once sign-out is unfinished', async () => {
  const server = durinServer();
  const { authorization } = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  await markedByAnotherProcess();
  await expect(
    auth(authorization as McpOAuthProvider, { serverUrl: resource, fetchFn: server.request }),
  ).rejects.toThrow(pendingMessage);
  expect(server.requests.filter(({ path }) => path === '/token')).toEqual([]);
  expect(server.requests.filter(usesOldGrant)).toEqual([]);
});

it('sends no request with the old grant from a transport outside the call gate', async () => {
  const server = durinServer();
  const { authorization } = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  await markedByAnotherProcess();
  const transport = new StreamableHTTPClientTransport(new URL(resource), {
    authProvider: authorization,
    fetch: server.request,
  });
  await transport.start();
  try {
    await expect(
      transport.send({ jsonrpc: '2.0', id: 7, method: 'tools/list', params: {} }),
    ).rejects.toThrow(pendingMessage);
    expect(server.requests.filter(usesOldGrant)).toEqual([]);
  } finally {
    await transport.close();
  }
});

// MAR-534 case 8: a grant the server revoked blocks the next MCP call, with no silent retry.
const signInAgain = (message: unknown) => {
  expect(message).toMatch(/sign in again/u);
  expect(message).toMatch(/Run durin in a terminal/u);
  expect(message).not.toMatch(/Durin/u);
};
const toolCall = (id: number) => ({
  jsonrpc: '2.0',
  id,
  method: 'tools/call',
  params: { name: 'sandbox__read_document', arguments: {} },
});

it('blocks the next bridge call once the server has revoked the grant, and asks to sign in again', async () => {
  const server = durinServer();
  const open = await openBridge(server);
  try {
    server.revoke();
    const revokedAt = server.requests.length;
    open.write(toolCall(2));
    const refused = (await open.next()) as { id: number; error?: { message: string } };
    expect(refused.id).toBe(2);
    signInAgain(refused.error?.message);
    const afterRefusal = server.requests.slice(revokedAt);
    expect(
      afterRefusal.filter(({ authorization }) => authorization === 'Bearer old-access'),
    ).toHaveLength(1);
    expect(afterRefusal.filter(({ path }) => path === '/token')).toHaveLength(1);

    const retriedAt = server.requests.length;
    open.write(toolCall(3));
    const again = (await open.next()) as { id: number; error?: { message: string } };
    expect(again.id).toBe(3);
    signInAgain(again.error?.message);
    expect(server.requests.slice(retriedAt).filter(usesOldGrant)).toEqual([]);
  } finally {
    await open.bridge.close();
  }
});

it('blocks durin tools once the server has revoked the grant, and asks to sign in again', async () => {
  const server = durinServer();
  server.revoke();
  vi.stubGlobal('fetch', server.request);
  vi.spyOn(process.stdout, 'write').mockImplementation(() => true);
  try {
    const tools = () => toolCommand(cliOptions(['tools', 'list', '--config-dir', directory]));
    const refused = await tools().catch((error: unknown) => error);
    expect(refused).toBeInstanceOf(CliError);
    signInAgain((refused as Error).message);
    const retriedAt = server.requests.length;
    const again = await tools().catch((error: unknown) => error);
    expect(again).toBeInstanceOf(CliError);
    signInAgain((again as Error).message);
    expect(server.requests.slice(retriedAt).filter(usesOldGrant)).toEqual([]);
  } finally {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  }
});
