import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, expect, it, vi } from 'vitest';
import { cliOptions } from './cli-options.js';
import { mcpCommand, verifyProfile } from './mcp-command.js';
import { logoutCommand, profileCommand } from './profile-commands.js';
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
const REVOKE = 'https://auth.getdurin.com/revoke';
const personal = `/mcp/u/${'a'.repeat(64)}`;
const directories: string[] = [];
afterEach(async () => {
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
const requests = () => {
  const seen: { url: string; body: string }[] = [];
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = input instanceof Request ? input.url : String(input);
    seen.push({ url, body: String(init?.body ?? '') });
    return new Response(null, { status: 200 });
  });
  return seen;
};

it('profiles list keeps a good profile and names a refused one without its tokens', async () => {
  const { directory } = await profileFile(`${PRODUCTION}/`);
  const good = {
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
  const goodId = createHash('sha256').update(JSON.stringify(good)).digest('hex');
  await writeFile(
    join(directory, 'profiles', `${goodId}.json`),
    JSON.stringify({ ...good, version: 1, id: goodId }),
  );
  const output = await printed(() =>
    profileCommand(cliOptions(['profiles', 'list', '--config-dir', directory])),
  );
  expect(output).toContain(goodId);
  expect(output).toContain('must be exactly auth.getdurin.com over HTTPS');
  expect(output).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
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

it('logout revokes an old production issuer at the pinned endpoint and empties the keyring', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`, {
    resource: `https://evil.example/mcp/u/${'a'.repeat(64)}`,
  });
  const seen = requests();
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(seen.map((call) => call.url)).toEqual([REVOKE]);
  expect(seen[0]?.body).toContain(REFRESH);
  expect(output).toContain('Run durin to sign in again.');
  expect(output).toContain('The server session was revoked.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(seen.some((call) => call.url.includes('evil.example'))).toBe(false);
  expect(keyring.size).toBe(0);
  await expect(readFile(join(directory, 'profiles', `${id}.json`), 'utf8')).rejects.toThrow();
});

it('logout deletes an untrusted issuer locally and says the server session was not revoked', async () => {
  const { directory, id } = await profileFile('https://evil.example/');
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
  expect(keyring.size).toBe(0);
});

it('logout still deletes local credentials when the production revoke call fails', async () => {
  const { directory, id } = await profileFile(`${PRODUCTION}/`);
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(null, { status: 500 }));
  const output = await printed(() =>
    logoutCommand(cliOptions(['logout', '--profile', id, '--config-dir', directory])),
  );
  expect(output).toContain('The server revoke failed');
  expect(output).toContain('Run durin to sign in again.');
  expect(output).not.toContain(ACCESS);
  expect(output).not.toContain(REFRESH);
  expect(keyring.size).toBe(0);
});
