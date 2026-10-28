import { execFile, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { afterAll, beforeAll, expect, it } from 'vitest';
import { build } from 'vite';

const exec = promisify(execFile);
// cli.test.ts empties and rebuilds dist/ while this file runs in parallel, so this file builds its
// own copy. It stays under node_modules so that the bundle's external packages still resolve.
let outDir = '';
beforeAll(async () => {
  await mkdir(join(process.cwd(), 'node_modules', '.cache'), { recursive: true });
  outDir = await mkdtemp(join(process.cwd(), 'node_modules', '.cache', 'durin-host-pin-e2e-'));
  await build({ logLevel: 'silent', build: { outDir } });
  // Under node_modules the bundle is outside this package's scope, so it names its module type.
  await writeFile(join(outDir, 'package.json'), '{"type":"module"}\n');
}, 60_000);
afterAll(async () => {
  await rm(outDir, { recursive: true, force: true });
});

/** Onboarding needs a terminal; util-linux `script` runs the built CLI in one. */
const terminal = spawnSync('script', ['--version'], { encoding: 'utf8' });
const hasTerminal = !terminal.error && terminal.stdout.includes('util-linux');
/** `true` and `1` are CI. `false`, `0`, empty, and unset are not, so they do not force these tests. */
const REAL_CI = new Set(['true', '1']);
const ciRunsHostPinE2e = (value: string | undefined): boolean =>
  REAL_CI.has(value?.trim().toLowerCase() ?? '');
const SCRIPT_REQUIRED = 'These end-to-end tests need util-linux `script`, which CI must install.';
/** The failure CI hits when `script` is missing. Any other `CI` value skips these tests instead. */
const hostPinTerminalFailure = (
  scriptPresent: boolean,
  env: NodeJS.ProcessEnv,
): string | undefined => {
  if (scriptPresent) return undefined;
  if (ciRunsHostPinE2e(env.CI)) return SCRIPT_REQUIRED;
  return undefined;
};
const terminalFailure = hostPinTerminalFailure(hasTerminal, process.env);
/** Without `script` these tests skip locally. Real CI runs them and fails when `script` is missing. */
const skipped = !hasTerminal && terminalFailure === undefined;

it.each([
  ['false', undefined],
  ['0', undefined],
  ['FALSE', undefined],
  ['', undefined],
  [undefined, undefined],
  ['true', SCRIPT_REQUIRED],
  ['1', SCRIPT_REQUIRED],
  ['TRUE', SCRIPT_REQUIRED],
] as const)(
  'with no script, CI=%j does not count as CI unless it is true or 1 (failure: %j)',
  (ci, failure) => {
    const env = { CI: ci };
    expect(hostPinTerminalFailure(false, env)).toBe(failure);
  },
);

it('runs the host-pin end-to-end tests when script is present, even if CI is false or 0', () => {
  expect(hostPinTerminalFailure(true, { CI: 'false' })).toBeUndefined();
  expect(hostPinTerminalFailure(true, { CI: '0' })).toBeUndefined();
});
const quoted = (value: string) => `'${value.replaceAll("'", `'\\''`)}'`;
/** Runs the built CLI with its own config directory and home, so no saved state is shared. */
const durin = async (args: readonly string[], prepare?: (home: string) => Promise<void>) => {
  if (terminalFailure) throw new Error(terminalFailure);
  const home = await mkdtemp(join(tmpdir(), 'durin-e2e-'));
  const command = [process.execPath, join(outDir, 'cli.js'), ...args, '--config-dir', home];
  try {
    if (prepare) await prepare(home);
    return await exec('script', ['-qec', command.map(quoted).join(' '), '/dev/null'], {
      env: { PATH: process.env.PATH, HOME: home, TERM: 'xterm' },
      timeout: 30_000,
    }).then(
      ({ stdout, stderr }) => ({ code: 0, output: `${stdout}${stderr ?? ''}` }),
      (error: { code: number; stdout: string; stderr?: string }) => ({
        code: error.code,
        output: `${error.stdout ?? ''}${error.stderr ?? ''}`,
      }),
    );
  } finally {
    await rm(home, { recursive: true, force: true });
  }
};

/**
 * A real HTTP server on 127.0.0.1 that records every request it receives, and counts every
 * connection, including one that never makes an HTTP request (such as a TLS handshake).
 */
const localServer = async (reply: (path: string, response: ServerResponse) => void) => {
  const hits: string[] = [];
  let connections = 0;
  const server = createServer((request, response) => {
    hits.push(`${request.method} ${request.url}`);
    reply(request.url ?? '', response);
  });
  server.on('connection', () => {
    connections += 1;
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;
  const close = () => {
    server.closeAllConnections();
    return new Promise<void>((resolve) => server.close(() => resolve()));
  };
  return { port, origin: `http://127.0.0.1:${port}`, hits, connections: () => connections, close };
};

it.skipIf(skipped)(
  'with --dev, the built CLI reads a local deployment’s config, then starts device sign-in on its WorkOS',
  { timeout: 30_000 },
  async () => {
    const workos = await localServer((_path, response) => {
      response.writeHead(503).end();
    });
    const app = await localServer((path, response) => {
      if (path !== '/auth/cli/config') return void response.writeHead(404).end();
      response.writeHead(200, { 'content-type': 'application/json' }).end(
        JSON.stringify({
          clientId: 'client_account',
          appOrigin: app.origin,
          issuer: `${app.origin}/`,
          providerOrigin: workos.origin,
        }),
      );
    });
    try {
      const { code, output } = await durin(['login', '--dev', '--app-url', app.origin]);
      expect(output).toContain('Unable to start account sign-in.');
      expect(code).toBe(1);
      expect(app.hits).toEqual(['GET /auth/cli/config']);
      expect(workos.hits).toEqual(['POST /user_management/authorize/device']);
    } finally {
      await Promise.all([app.close(), workos.close()]);
    }
  },
);

it.skipIf(skipped)(
  'the built CLI accepts a no-slash self-hosted profile and refuses an old production issuer without connecting',
  { timeout: 30_000 },
  async () => {
    const stub = await localServer((_path, response) => {
      response.writeHead(200).end('should-not-be-called');
    });
    const refused = {
      appOrigin: 'https://app.getdurin.com',
      accountIssuer: 'https://auth.getdurin.com/',
      accountClientId: 'bearer-OLD-ACCESS-9f3c',
      principalId: 'user_one',
      workosOrganizationId: 'org_one',
      organizationId: 'org_internal',
      environment: 'production' as const,
      resource: `https://mcp.getdurin.com/mcp/u/${'a'.repeat(64)}`,
      mcpIssuer: 'https://auth.getdurin.com/',
      development: false,
    };
    const accepted = {
      ...refused,
      appOrigin: 'https://durin.acme.test',
      accountIssuer: 'https://durin.acme.test',
      accountClientId: 'account',
      resource: `https://durin.acme.test/mcp/u/${'b'.repeat(64)}`,
      mcpIssuer: 'https://durin.acme.test',
    };
    const writeProfile = async (home: string, scope: typeof refused) => {
      const id = createHash('sha256').update(JSON.stringify(scope)).digest('hex');
      await mkdir(join(home, 'profiles'), { recursive: true });
      await writeFile(join(home, 'active'), `${id}\n`);
      await writeFile(
        join(home, 'profiles', `${id}.json`),
        JSON.stringify({ ...scope, version: 1, id }),
      );
    };
    try {
      const refusedRun = await durin(['tools', 'list'], (home) => writeProfile(home, refused));
      expect(refusedRun.code).toBe(1);
      expect(refusedRun.output).toContain('must be exactly auth.getdurin.com over HTTPS');
      expect(refusedRun.output).toContain('Run durin to sign in again.');
      expect(refusedRun.output).not.toContain('bearer-OLD-ACCESS-9f3c');
      expect(stub.hits).toEqual([]);
      expect(stub.connections()).toBe(0);
      const acceptedRun = await durin(['tools', 'list'], (home) => writeProfile(home, accepted));
      expect(acceptedRun.output).not.toContain('must be exactly auth.getdurin.com');
      expect(acceptedRun.output).not.toContain("issuer isn't trusted");
      expect(stub.hits).toEqual([]);
      expect(stub.connections()).toBe(0);
    } finally {
      await stub.close();
    }
  },
);

it.skipIf(skipped)(
  'without --dev, the built CLI refuses a loopback app URL before connecting to it',
  { timeout: 30_000 },
  async () => {
    const app = await localServer((_path, response) => {
      response.writeHead(503).end();
    });
    try {
      const host = `127.0.0.1:${app.port}`;
      const { code, output } = await durin(['login', '--app-url', `https://${host}`]);
      expect(output).toContain(`durin didn’t contact ${host}.`);
      expect(output).toContain('Local addresses are only allowed with --dev.');
      expect(code).toBe(1);
      expect(app.hits).toEqual([]);
      // The CLI would speak HTTPS to this plain HTTP server, so no request could ever arrive;
      // only a connection count shows that it never tried.
      expect(app.connections()).toBe(0);
    } finally {
      await app.close();
    }
  },
);
