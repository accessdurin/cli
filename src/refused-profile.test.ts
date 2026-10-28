import { createHash } from 'node:crypto';
import { access, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { afterEach, expect, it, vi } from 'vitest';
import { cliOptions } from './cli-options.js';
import { mcpCommand, verifyProfile } from './mcp-command.js';
import { logoutCommand, profileCommand } from './profile-commands.js';
import { withProfileLock } from './profile-lock.js';
import { createProfile } from './profiles.js';
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
vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = async () => {};
    close = async () => {};
    listTools = async () => ({ tools: [] });
  },
}));

const ACCESS = 'bearer-OLD-ACCESS-9f3c';
const REFRESH = 'refresh-OLD-9f3c';
const PRODUCTION = 'https://auth.getdurin.com';
const personal = `/mcp/u/${'a'.repeat(64)}`;
const directories: string[] = [];
afterEach(async () => {
  process.exitCode = undefined;
  keyring.clear();
  vi.restoreAllMocks();
  await Promise.all(
    directories.splice(0).map((path) => rm(path, { recursive: true, force: true })),
  );
});

const profileFile = async (
  issuer: string,
  extra: Record<string, string> = {},
): Promise<{ directory: string; id: string }> => {
  const scope = {
    appOrigin: 'https://app.getdurin.com',
    accountIssuer: issuer,
    accountClientId: `${ACCESS} ${REFRESH}`,
    principalId: 'user_one',
    workosOrganizationId: 'org_one',
    organizationId: 'org_internal',
    environment: 'production' as const,
    resource: `https://mcp.getdurin.com${personal}`,
    mcpIssuer: issuer,
    development: false,
    ...extra,
  };
  const id = createHash('sha256').update(JSON.stringify(scope)).digest('hex');
  const directory = await mkdtemp(join(tmpdir(), 'durin-refused-'));
  directories.push(directory);
  await mkdir(join(directory, 'profiles'), { recursive: true });
  await writeFile(join(directory, 'active'), `${id}\n`);
  await writeFile(
    join(directory, 'profiles', `${id}.json`),
    JSON.stringify({ ...scope, version: 1, id }),
  );
  keyring.set(
    `mcp:${id}:tokens`,
    JSON.stringify({ access_token: ACCESS, refresh_token: REFRESH, token_type: 'Bearer' }),
  );
  keyring.set(`mcp:${id}:client`, JSON.stringify({ client_id: 'client_one' }));
  return { directory, id };
};
const printed = async (run: () => Promise<void>): Promise<string> => {
  const chunks: string[] = [];
  const write = vi.spyOn(process.stdout, 'write').mockImplementation((chunk) => {
    chunks.push(String(chunk));
    return true;
  });
  try {
    await run();
  } finally {
    write.mockRestore();
  }
  return chunks.join('');
};
type Seen = {
  url: string;
  body: string;
  redirect: RequestInit['redirect'] | undefined;
  signal: AbortSignal | undefined;
};
const callUrl = (input: string | URL | Request): string => {
  if (input instanceof Request) return input.url;
  return String(input);
};
const callBody = (init: RequestInit | undefined): string => {
  if (!init?.body) return '';
  return String(init.body);
};
const callRedirect = (init: RequestInit | undefined): RequestInit['redirect'] | undefined => {
  if (!init) return undefined;
  return init.redirect;
};
const callSignal = (init: RequestInit | undefined): AbortSignal | undefined => {
  if (!init) return undefined;
  return init.signal ?? undefined;
};
const answer = (reply: ((url: string) => Response) | undefined, url: string): Response => {
  if (reply) return reply(url);
  return new Response(null, { status: 200 });
};
const requests = (reply?: (url: string) => Response) => {
  const seen: Seen[] = [];
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = callUrl(input);
    seen.push({
      url,
      body: callBody(init),
      redirect: callRedirect(init),
      signal: callSignal(init),
    });
    return answer(reply, url);
  });
  return seen;
};
const first = (calls: readonly Seen[]): Seen => {
  const call = calls[0];
  if (!call) throw new Error('expected a request');
  return call;
};
const PRODUCTION_METADATA = {
  issuer: PRODUCTION,
  authorization_endpoint: `${PRODUCTION}/oauth2/authorize`,
  token_endpoint: `${PRODUCTION}/oauth2/token`,
  response_types_supported: ['code'],
};
const metadataReply =
  (revocation?: string) =>
  (url: string): Response => {
    if (url.includes('/revoke')) return new Response(null, { status: 200 });
    const extra = revocation ? { revocation_endpoint: revocation } : {};
    return Response.json({ ...PRODUCTION_METADATA, ...extra });
  };
const seedUnrevoked = async (directory: string, id: string): Promise<void> => {
  const grant = '11111111-1111-4111-8111-111111111111';
  keyring.set(
    `mcp:${id}:unrevoked:${grant}`,
    JSON.stringify({ accessToken: ACCESS, refreshToken: REFRESH }),
  );
  await mkdir(join(directory, 'revocations'), { recursive: true });
  await writeFile(
    join(directory, 'revocations', `${id}.unrevoked.json`),
    `${JSON.stringify({
      grants: [{ id: grant, clientId: 'client_one', addedAt: '2026-01-01T00:00:00.000Z' }],
    })}\n`,
  );
  await writeFile(join(directory, 'revocations', `${id}.pending`), '{}\n');
};
const waitForLock = async (lock: string, attempt = 50): Promise<void> => {
  const held = await access(lock).then(
    () => true,
    () => false,
  );
  if (held) return;
  if (attempt <= 1) throw new Error('refused logout did not take the profile lock');
  await setTimeout(20);
  return waitForLock(lock, attempt - 1);
};
const nothingLeft = async (directory: string, id: string): Promise<void> => {
  expect([...keyring.keys()].some((key) => key.includes(id))).toBe(false);
  const names = await readdir(join(directory, 'revocations')).catch(() => [] as string[]);
  expect(names.some((name) => name.includes(id))).toBe(false);
};
const goodProfile = {
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://app.example.test',
  accountClientId: 'account',
  principalId: 'user_two',
  workosOrganizationId: 'org_two',
  organizationId: 'org_other',
  environment: 'production' as const,
  resource: `https://app.example.test/mcp/u/${'b'.repeat(64)}`,
  mcpIssuer: 'https://app.example.test',
  development: false,
};
const writeGood = async (directory: string): Promise<string> => {
  const saved = createProfile(goodProfile);
  await writeFile(join(directory, 'profiles', `${saved.id}.json`), JSON.stringify(saved));
  return saved.id;
};

it('profiles list keeps a good profile and names a refused one without its tokens or a request', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  const goodId = await writeGood(directory);
  const seen = requests();
  const output = await printed(() =>
    profileCommand(cliOptions(['profiles', 'list', '--config-dir', directory])),
  );
  const parsed: unknown = JSON.parse(output);
  expect(Array.isArray(parsed)).toBe(true);
  const rows = parsed as { id?: string; refused?: boolean; reason?: string }[];
  expect(rows).toContainEqual(createProfile(goodProfile));
  const accepted = rows.find((entry) => entry.id === goodId);
  expect(accepted && 'refused' in accepted).toBe(false);
  const refused = rows.find((entry) => entry.id === id);
  expect(refused).toMatchObject({ id, refused: true });
  expect(refused?.reason).toContain('must be exactly auth.getdurin.com over HTTPS');
  expect(refused?.reason).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(seen).toEqual([]);
});

it('profiles list prints an all-accepted list as a flat array of profiles', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-refused-'));
  directories.push(directory);
  await mkdir(join(directory, 'profiles'), { recursive: true });
  await writeGood(directory);
  const output = await printed(() =>
    profileCommand(cliOptions(['profiles', 'list', '--config-dir', directory])),
  );
  expect(JSON.parse(output)).toEqual([createProfile(goodProfile)]);
});

it.each([
  [
    'tools list',
    (directory: string) => toolCommand(cliOptions(['tools', 'list', '--config-dir', directory])),
  ],
  [
    'verify',
    (directory: string) => verifyProfile(cliOptions(['verify', '--config-dir', directory])),
  ],
  [
    'mcp serve',
    (directory: string) => mcpCommand(cliOptions(['mcp', 'serve', '--config-dir', directory])),
  ],
])('%s names the refused issuer and makes no request', async (_name, run) => {
  const { directory } = await profileFile(`${PRODUCTION}/`);
  const seen = requests();
  await expect(run(directory)).rejects.toThrow('must be exactly auth.getdurin.com over HTTPS');
  await expect(run(directory)).rejects.toThrow('Run durin to sign in again.');
  const message = await run(directory).then(
    () => '',
    (error: unknown) => String(error),
  );
  expect(message).not.toContain(ACCESS);
  expect(message).not.toContain(REFRESH);
  expect(seen).toEqual([]);
});

it('logout does not revoke when the resource is not production, and removes pending revocations', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`, {
    resource: `https://evil.example/mcp/u/${'a'.repeat(64)}`,
  });
  await seedUnrevoked(directory, id);
  const seen = requests();
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(seen).toEqual([]);
  expect(output).toContain(
    `Removed local credentials for ${id}. The server session was not revoked because its issuer isn't trusted; it will expire on its own.`,
  );
  expect(output).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  await nothingLeft(directory, id);
  await expect(readFile(join(directory, 'profiles', `${id}.json`), 'utf8')).rejects.toThrow();
});

it.each([
  ['a lookalike host', 'https://auth.getdurin.com.evil.example'],
  ['userinfo', 'https://auth.getdurin.com@evil.example'],
  ['uppercase', 'HTTPS://AUTH.getdurin.com/'],
])('logout makes no request when the issuer is %s', async (_name, issuer) => {
  const { directory, id } = await profileFile(issuer);
  const seen = requests();
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(seen).toEqual([]);
  expect(output).toContain("The server session was not revoked because its issuer isn't trusted");
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  expect([...keyring.keys()].some((key) => key.includes(id))).toBe(false);
});

it('logout makes no request when a self-hosted app claims the production issuer', async () => {
  const { directory, id } = await profileFile(PRODUCTION, {
    appOrigin: 'https://durin.acme.test',
    resource: `https://durin.acme.test/mcp/u/${'a'.repeat(64)}`,
  });
  const seen = requests();
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(seen).toEqual([]);
  expect(output).toContain("The server session was not revoked because its issuer isn't trusted");
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  expect(keyring.size).toBe(0);
});

it('logout deletes an untrusted issuer locally and says the server session was not revoked', async () => {
  const { directory, id } = await profileFile('https://evil.example/');
  await seedUnrevoked(directory, id);
  const seen = requests();
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--config-dir', directory])),
  );
  expect(seen).toEqual([]);
  expect(output).toContain(
    `Removed local credentials for ${id}. The server session was not revoked because its issuer isn't trusted; it will expire on its own.`,
  );
  expect(output).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  await nothingLeft(directory, id);
});

it('logout of an old production profile does not post a token when revocation is not advertised', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  await seedUnrevoked(directory, id);
  const seen = requests(metadataReply());
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(seen.map((call) => call.url)).toEqual([
    'https://auth.getdurin.com/.well-known/oauth-authorization-server',
  ]);
  expect(seen.every((call) => call.redirect === 'error')).toBe(true);
  expect(seen.every((call) => call.signal instanceof AbortSignal)).toBe(true);
  expect(seen.every((call) => call.body === '')).toBe(true);
  expect(output).toContain(
    `Removed local credentials for ${id}. ${'The server session was not revoked; it will expire on its own.'}`,
  );
  expect(output).not.toContain('The server session was revoked.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  await nothingLeft(directory, id);
});

it('logout posts the stored client and refresh hint only to an advertised production revocation endpoint', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  const endpoint = `${PRODUCTION}/oauth2/revoke`;
  const seen = requests(metadataReply(endpoint));
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  const posts = seen.filter((call) => call.body.includes(REFRESH));
  const post = first(posts);
  expect(posts.map((call) => call.url)).toEqual([endpoint]);
  expect(post.body).toContain('client_id=client_one');
  expect(post.body).toContain('token_type_hint=refresh_token');
  expect(post.body).not.toContain('client_id=durin');
  expect(post.redirect).toBe('error');
  expect(seen.some((call) => call.url.includes('evil.example'))).toBe(false);
  expect(output).toContain('The server session was revoked.');
  expect(output).not.toContain(ACCESS);
  expect(process.exitCode).toBeUndefined();
  expect(keyring.size).toBe(0);
});

it('logout sends an access token with the access-token hint when no refresh token is stored', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  keyring.set(`mcp:${id}:tokens`, JSON.stringify({ access_token: ACCESS, token_type: 'Bearer' }));
  const endpoint = `${PRODUCTION}/oauth2/revoke`;
  const seen = requests(metadataReply(endpoint));
  await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  const post = first(seen.filter((call) => call.url === endpoint));
  expect(post.body).toContain(ACCESS);
  expect(post.body).toContain('token_type_hint=access_token');
  expect(post.body).toContain('client_id=client_one');
  expect(post.body).not.toContain('refresh_token');
});

it('logout still deletes local credentials when the production revoke call fails', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  const endpoint = `${PRODUCTION}/oauth2/revoke`;
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = input instanceof Request ? input.url : String(input);
    if (url === endpoint) return new Response(null, { status: 500 });
    return Response.json({ ...PRODUCTION_METADATA, revocation_endpoint: endpoint });
  });
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(output).toContain('The server session was not revoked; it will expire on its own.');
  expect(output).not.toContain('The server revoke failed');
  expect(output).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(process.exitCode).toBeUndefined();
  expect(keyring.size).toBe(0);
});

it('logout of a refused profile holds the profile lock until it finishes', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  let release = (): void => undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
    await gate;
    return Response.json(PRODUCTION_METADATA);
  });
  const logout = printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  await waitForLock(join(directory, 'locks', `${id}.lock`));
  let entered = false;
  const waiting = withProfileLock(directory, id, async () => {
    entered = true;
  });
  await setTimeout(250);
  expect(entered).toBe(false);
  release();
  await logout;
  await waiting;
  expect(entered).toBe(true);
});
