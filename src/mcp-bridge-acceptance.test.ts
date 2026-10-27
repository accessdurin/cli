// MAR-552 acceptance (Tess's QA repro of Seth's review): a running bridge must stop relaying its
// server event stream once a failed logout in another process sets the marker.
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { PassThrough } from 'node:stream';
import { setTimeout } from 'node:timers/promises';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { cliOptions } from './cli-options.js';
import { signOutMcp } from './logout.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { profileConnection } from './profile-client.js';
import { withProfileLock } from './profile-lock.js';
import { createProfile, FileProfiles } from './profiles.js';
import { FileRevocationMarker } from './revocation-marker.js';

const scope = {
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://app.example.test',
  accountClientId: 'account',
  principalId: 'user_one',
  workosOrganizationId: 'org_one',
  organizationId: 'org_internal',
  environment: 'production',
  resource: `https://app.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://app.example.test',
  development: false,
} as const;
const profile = createProfile(scope);
let directory = '';
beforeEach(async () => {
  directory = await mkdtemp(join(tmpdir(), 'qa-bridge-'));
  const profiles = new FileProfiles(directory);
  await profiles.save(profile);
  await profiles.activate(profile.id);
});
afterEach(async () => {
  vi.restoreAllMocks();
  await rm(directory, { recursive: true, force: true });
});
const memoryStore = (values: Map<string, string>) => ({
  read: async (k: string) => values.get(k) ?? null,
  write: async (k: string, v: string) => {
    values.set(k, v);
  },
  remove: async (k: string) => {
    values.delete(k);
  },
});

it('SETH-M2: a running bridge stops relaying the server event stream once a failed logout sets the marker', async () => {
  const values = new Map<string, string>();
  const provider = new McpOAuthProvider({ profile, store: memoryStore(values) });
  await provider.saveClientInformation({ client_id: 'client_one', issuer: profile.mcpIssuer });
  await provider.saveTokens({
    access_token: 'synthetic-access',
    refresh_token: 'synthetic-refresh',
    token_type: 'Bearer',
    issuer: profile.mcpIssuer,
  });
  const input = new PassThrough();
  const output = new PassThrough();
  const received: string[] = [];
  output.on('data', (chunk) => received.push(...String(chunk).split('\n').filter(Boolean)));
  let push = (_: string) => {};
  let tokenCalls = 0;
  const eventStream = () => {
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        push = (s) => c.enqueue(new TextEncoder().encode(s));
      },
    });
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'text/event-stream' },
    });
  };
  const answer = (raw: string) => {
    const message = JSON.parse(raw) as { id?: number; method?: string };
    if (message.id === undefined) return new Response(null, { status: 202 });
    return Response.json(
      {
        jsonrpc: '2.0',
        id: message.id,
        result: {
          protocolVersion: '2025-06-18',
          capabilities: {},
          serverInfo: { name: 's', version: '1' },
        },
      },
      { headers: { 'mcp-session-id': 'sess' } },
    );
  };
  const connection = await profileConnection(
    cliOptions(['mcp', 'serve', '--config-dir', directory]),
  );
  const bridge = new McpStdioBridge({
    ...connection,
    authorization: {
      token: async () => {
        tokenCalls += 1;
        return 'synthetic-access';
      },
    },
    fetch: async (_url, init = {}) =>
      [undefined, 'GET'].includes(init.method) ? eventStream() : answer(String(init.body)),
    input,
    output,
  });
  await bridge.start();
  try {
    input.write(
      `${JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'c', version: '1' } } })}\n`,
    );
    input.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' })}\n`);
    await setTimeout(200);
    // Another process: logout fails (503) -> marker set, tokens kept.
    const result = await withProfileLock(directory, profile.id, () =>
      signOutMcp({
        profile,
        store: memoryStore(values),
        marker: new FileRevocationMarker(directory, profile.id),
        request: async (url) =>
          String(url).includes('.well-known')
            ? Response.json({
                issuer: profile.mcpIssuer,
                authorization_endpoint: 'https://app.example.test/a',
                token_endpoint: 'https://app.example.test/t',
                response_types_supported: ['code'],
                revocation_endpoint: 'https://app.example.test/revoke',
              })
            : new Response(null, { status: 503 }),
      }),
    );
    expect(result).toEqual({ remote: 'failed', local: 'retained' });
    expect(await new FileRevocationMarker(directory, profile.id).pending()).toBe(true);
    const before = received.length;
    push(
      `event: message\ndata: ${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/message', params: { level: 'info', data: 'server push after marker' } })}\n\n`,
    );
    await setTimeout(200);
    const after = received.slice(before);
    expect(after).toEqual([]);
  } finally {
    await bridge.close();
  }
});
