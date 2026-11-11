import { execFile, spawnSync } from 'node:child_process';
import { chmod, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { beforeAll, describe, expect, it } from 'vitest';
import { build } from 'vite';
import { installRuntime } from './install-runtime.js';
import { installHooks } from './install-hooks.js';
import { agentsFor } from './agents.js';

const exec = promisify(execFile);
// All built-CLI tests share this build; the remaining suites import the source directly.
beforeAll(async () => {
  await build({ logLevel: 'silent' });
}, 60_000);

describe.concurrent('CLI public interface', () => {
  it('requires a terminal for onboarding and documents all three access paths from an isolated runtime', async () => {
    await expect(exec(process.execPath, ['dist/cli.js'])).rejects.toMatchObject({
      code: 1,
      stderr: expect.stringContaining('terminal'),
    });
    const directory = await mkdtemp(join(tmpdir(), 'durin-cli-runtime-'));
    try {
      const runtime = await installRuntime(directory, join(process.cwd(), 'dist/cli.js'));
      const { stdout, stderr } = await exec(process.execPath, [runtime, '--help']);
      expect(stdout).toContain('tools list');
      expect(stdout).toContain('mcp serve');
      expect(stdout).toContain('mcp endpoint');
      expect(stderr).toBe('');
      const checkerPath = join(directory, 'checker.mjs');
      await writeFile(
        checkerPath,
        "let input=''; for await (const part of process.stdin) input+=part; process.exit(JSON.parse(input).tool_input.deny ? 2 : 0);",
      );
      await installHooks(
        agentsFor(directory).claude!,
        {
          nodePath: process.execPath,
          cliPath: runtime,
          profileId: 'a'.repeat(64),
          configDirectory: directory,
        },
        { command: process.execPath, args: [checkerPath], timeoutMs: 1000 },
      );
      const input = {
        hook_event_name: 'PreToolUse',
        session_id: 's',
        tool_use_id: 't',
        tool_name: 'Bash',
        tool_input: {},
      };
      const run = (deny: boolean) => {
        const request = exec(process.execPath, [
          runtime,
          'hooks',
          'run',
          '--harness',
          'claude',
          '--config-dir',
          directory,
        ]);
        request.child.stdin!.end(JSON.stringify({ ...input, tool_input: { deny } }));
        return request;
      };
      await expect(run(false)).resolves.toMatchObject({ stdout: '', stderr: '' });
      await expect(run(true)).rejects.toMatchObject({
        code: 2,
        stderr: expect.stringContaining('denied'),
      });
      const status = await exec(process.execPath, [
        runtime,
        'hooks',
        'status',
        '--config-dir',
        directory,
      ]);
      expect(JSON.parse(status.stdout)).toMatchObject([
        {
          harness: 'claude',
          checker: 'available',
          configuration: 'installed',
          execution: 'not-verified',
        },
      ]);
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
  it.each([
    ['tools', 'list', 'extra'],
    ['tools', 'list', '--input', 'unused.json'],
    ['login', '--profile', 'a'.repeat(64)],
    ['mcp', 'serve', '--dev'],
    ['tools', 'list', '--force'],
  ])('rejects unused arguments: %j', async (...args) => {
    await expect(exec(process.execPath, ['dist/cli.js', ...args])).rejects.toMatchObject({
      code: 1,
      stderr: expect.stringContaining('Invalid arguments'),
    });
  });
  it.skipIf(process.platform === 'win32')(
    'refuses a configuration directory other users can write',
    async () => {
      const directory = await mkdtemp(join(tmpdir(), 'durin-cli-'));
      try {
        await chmod(directory, 0o777);
        await expect(
          exec(process.execPath, ['dist/cli.js', 'profiles', 'list', '--config-dir', directory]),
        ).rejects.toMatchObject({ code: 1, stderr: expect.stringContaining('chmod 700') });
      } finally {
        await rm(directory, { recursive: true, force: true });
      }
    },
  );
  it('accepts --force for logout', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'durin-cli-'));
    try {
      const failure = await exec(process.execPath, [
        'dist/cli.js',
        'logout',
        '--force',
        '--config-dir',
        directory,
      ]).catch((error: { stderr: string }) => error);
      expect(failure).toMatchObject({
        stderr: expect.not.stringMatching(/Unknown option|Invalid arguments/u),
      });
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
  it.each(['--agent', '-a'])('rejects the removed %s option before onboarding', async (flag) => {
    await expect(
      exec(process.execPath, ['dist/cli.js', '--help', flag, 'claude']),
    ).rejects.toMatchObject({ code: 1, stderr: expect.stringContaining('Unknown option') });
  });
});

/** Onboarding needs a terminal; util-linux `script` runs the built CLI in one. */
const terminal = spawnSync('script', ['--version'], { encoding: 'utf8' });
const hasTerminal = !terminal.error && terminal.stdout.includes('util-linux');
/** Without `script` these tests skip locally, but fail in CI, which must run them. */
const skipped = !hasTerminal && !process.env.CI;
const quoted = (value: string) => `'${value.replaceAll("'", `'\\''`)}'`;
/** Runs the built CLI with its own config directory and home, so no saved state is shared. */
const durin = async (args: readonly string[]) => {
  if (!hasTerminal)
    throw new Error('These end-to-end tests need util-linux `script`, which CI must install.');
  const home = await mkdtemp(join(tmpdir(), 'durin-e2e-'));
  const command = [
    process.execPath,
    join(process.cwd(), 'dist', 'cli.js'),
    ...args,
    '--config-dir',
    home,
  ];
  try {
    return await exec('script', ['-qec', command.map(quoted).join(' '), '/dev/null'], {
      env: { PATH: process.env.PATH, HOME: home, TERM: 'xterm', NODE_ENV: 'test' },
      timeout: 30_000,
    }).then(
      ({ stdout }) => ({ code: 0, output: stdout }),
      (error: { code: number; stdout: string }) => ({ code: error.code, output: error.stdout }),
    );
  } finally {
    await rm(home, { recursive: true, force: true });
  }
};

/** Records requests and connections, including attempted TLS handshakes. */
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
      // Only the connection count can detect an attempted TLS handshake with this HTTP server.
      expect(app.connections()).toBe(0);
    } finally {
      await app.close();
    }
  },
);
