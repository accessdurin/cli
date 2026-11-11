import { mkdtemp, mkdir, readFile, readdir, rm, writeFile, symlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { Readable } from 'node:stream';
import { spawn } from 'node:child_process';
import { afterEach, expect, it } from 'vitest';
import { agentsFor, type HookFormat } from './agents.js';
import { installSkills } from './install-skills.js';
import { installHooks } from './install-hooks.js';
import { readHookInstallation, runHook } from './hook-runner.js';
import { readCustomHarness } from './custom-harness.js';
import { updatedHookConfig } from './hook-config.js';
import { planManagedFile, saveFileChange } from './managed-files.js';

const temporary: string[] = [];
afterEach(async () => {
  await Promise.all(temporary.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});
const fixture = async () => {
  const home = await mkdtemp(join(tmpdir(), 'durin-integrations-'));
  temporary.push(home);
  const checkerFile = join(home, 'checker.mjs');
  const capture = join(home, 'captured.json');
  await writeFile(
    checkerFile,
    `import { writeFileSync } from 'node:fs';
let raw = ''; for await (const chunk of process.stdin) raw += chunk;
const event = JSON.parse(raw); writeFileSync(${JSON.stringify(capture)}, JSON.stringify(event));
if (event.tool_input.wait) await new Promise(resolve => setTimeout(resolve, 500));
process.exit(event.tool_input.code ?? 0);`,
  );
  return {
    home,
    capture,
    checker: { command: process.execPath, args: [checkerFile], timeoutMs: 1000 },
    bridge: {
      nodePath: process.execPath,
      cliPath: join(home, 'runtime.mjs'),
      profileId: 'a'.repeat(64),
      configDirectory: join(home, 'durin'),
    },
  };
};
const canonical = {
  hook_event_name: 'PreToolUse',
  session_id: 'session',
  tool_use_id: 'call',
  tool_name: 'mcp__sandbox__read',
  tool_input: { resource: 'handbook' },
};
const inputFor = (format: HookFormat) => {
  if (format === 'windsurf')
    return {
      agent_action_name: 'pre_mcp_tool_use',
      session_id: 'session',
      tool_use_id: 'call',
      tool_info: {
        mcp_server_name: 'sandbox',
        mcp_tool_name: 'read',
        mcp_tool_arguments: canonical.tool_input,
      },
    };
  if (format === 'copilot')
    return {
      sessionId: 'session',
      toolCallId: 'call',
      toolName: canonical.tool_name,
      toolArgs: JSON.stringify(canonical.tool_input),
    };
  return canonical;
};
const formats = [
  'claude',
  'codex',
  'cursor',
  'windsurf',
  'opencode',
  'gemini',
  'copilot',
  'custom',
] as const;
it.each(formats)(
  'installs %s skills/hooks idempotently, preserves settings, and runs a real local checker',
  async (format) => {
    const f = await fixture();
    const agent =
      format === 'custom'
        ? {
            ...agentsFor(f.home).claude!,
            id: 'custom',
            skillsDirectory: join(f.home, 'custom-skills'),
            hookConfig: { path: join(f.home, 'custom-hooks.json'), format },
          }
        : agentsFor(f.home)[format]!;
    expect(await installSkills(agent)).toBe(5);
    expect(await installSkills(agent)).toBe(5);
    const skills = await readdir(agent.skillsDirectory!);
    expect(skills).toEqual(
      expect.arrayContaining([
        'durin-setup',
        'durin-tools',
        'durin-verify',
        'durin-hooks',
        'durin-profiles',
      ]),
    );
    const target = agent.hookConfig!.path;
    if (format !== 'opencode') {
      await mkdir(dirname(target), { recursive: true });
      await writeFile(target, '{ // preserve this comment\n"theme":"keep","hooks":{}}');
    }
    await installHooks(agent, f.bridge, f.checker);
    const saved = await readFile(target, 'utf8');
    await installHooks(agent, f.bridge, f.checker);
    expect(await readFile(target, 'utf8')).toBe(saved);
    const markers =
      format === 'opencode' ? ['Managed by Durin CLI'] : ['preserve this comment', '"keep"'];
    expect(markers.every((marker) => saved.includes(marker))).toBe(true);
    const installation = await readHookInstallation(f.bridge.configDirectory, agent.id!);
    await runHook(installation, Readable.from([JSON.stringify(inputFor(format))]));
    expect(JSON.parse(await readFile(f.capture, 'utf8'))).toMatchObject({
      ...canonical,
      durin_harness: agent.id,
    });
    expect(saved).not.toContain('resource');
  },
);

it('keeps mixed third-party hook groups and refuses disabled or malformed configurations', async () => {
  const { bridge } = await fixture();
  const input = { format: 'claude' as const, bridge, harness: 'claude', timeoutMs: 1000 };
  const raw =
    '{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":/* keep inner handler comment */"existing-check"}]}],"PostToolUse":[{"matcher":".*","hooks":[]}]}}';
  const rendered = updatedHookConfig(raw, input);
  expect(rendered).toContain('keep inner handler comment');
  const saved = JSON.parse(rendered.replace('/* keep inner handler comment */', ''));
  expect(saved.hooks.PreToolUse[0]).toEqual(
    JSON.parse(raw.replace('/* keep inner handler comment */', '')).hooks.PreToolUse[0],
  );
  expect(saved.hooks.PostToolUse).toEqual(
    JSON.parse(raw.replace('/* keep inner handler comment */', '')).hooks.PostToolUse,
  );
  for (const invalid of [
    '{"disableAllHooks":true}',
    '{"hooksConfig":{"enabled":false}}',
    '{"hooks":{"PreToolUse":{}}}',
    '{',
  ]) {
    expect(() => updatedHookConfig(invalid, input)).toThrow();
  }
  const windows = JSON.parse(updatedHookConfig('{}', { ...input, windows: true }));
  const command = windows.hooks.PreToolUse[0].hooks[0].command as string;
  expect(command).toContain(bridge.nodePath);
  expect(command).toContain('exit $LASTEXITCODE');
});

it('preserves edited skills/plugins and rejects changed files and symlink targets before replacement', async () => {
  const f = await fixture();
  const agent = agentsFor(f.home).claude!;
  await installSkills(agent);
  const path = join(agent.skillsDirectory!, 'durin-tools', 'SKILL.md');
  await writeFile(path, 'user-edited skill');
  await expect(installSkills(agent)).rejects.toThrow('Preserving');
  expect(await readFile(path, 'utf8')).toBe('user-edited skill');
  const changed = join(f.home, 'changed.txt');
  const plan = await planManagedFile(changed, 'planned');
  await writeFile(changed, 'concurrent edit');
  await expect(saveFileChange(plan)).rejects.toThrow('changed');
  expect(await readFile(changed, 'utf8')).toBe('concurrent edit');
  const opencode = agentsFor(f.home).opencode!;
  await mkdir(dirname(opencode.hookConfig!.path), { recursive: true });
  await writeFile(opencode.hookConfig!.path, '// user plugin');
  await expect(installHooks(opencode, f.bridge, f.checker)).rejects.toThrow('Preserving');
});

it.skipIf(process.platform === 'win32')(
  'rejects a symlink destination instead of replacing its target',
  async () => {
    const f = await fixture();
    const changed = join(f.home, 'original.txt');
    const link = join(f.home, 'link.txt');
    await writeFile(changed, 'original');
    await symlink(changed, link);
    await expect(planManagedFile(link, 'replacement')).rejects.toThrow('regular file');
    expect(await readFile(changed, 'utf8')).toBe('original');
  },
);

it.each([
  { input: { code: 2 }, message: 'denied' },
  { input: { code: 1 }, message: 'failed' },
  { input: { wait: true }, message: 'timed out' },
])('blocks checker outcome $message without exposing input', async ({ input, message }) => {
  const f = await fixture();
  await installHooks(agentsFor(f.home).claude!, f.bridge, {
    ...f.checker,
    timeoutMs: input.wait ? 100 : 1000,
  });
  const installation = await readHookInstallation(f.bridge.configDirectory, 'claude');
  await expect(
    runHook(installation, Readable.from([JSON.stringify({ ...canonical, tool_input: input })])),
  ).rejects.toThrow(message);
});

it('rejects invalid/oversized input before running a checker and validates custom descriptors', async () => {
  const f = await fixture();
  await installHooks(agentsFor(f.home).claude!, f.bridge, f.checker);
  const installation = await readHookInstallation(f.bridge.configDirectory, 'claude');
  const invalidInputs = [
    '{}',
    JSON.stringify({ ...canonical, tool_input: 'not an object' }),
    'x'.repeat(1_048_577),
  ];
  await Promise.all(
    invalidInputs.map((input) =>
      expect(runHook(installation, Readable.from([input]))).rejects.toThrow(),
    ),
  );
  await expect(readFile(f.capture)).rejects.toMatchObject({ code: 'ENOENT' });
  const path = join(f.home, 'custom.json');
  const descriptor = {
    id: 'custom-test',
    name: 'My harness',
    configPath: join(f.home, 'agent.json'),
    format: 'jsonc',
    serverKey: 'mcpServers',
    bridge: 'arguments',
    skillsDirectory: join(f.home, 'skills'),
    hookConfig: { path: join(f.home, 'hooks.json'), format: 'custom' },
  };
  await writeFile(path, JSON.stringify(descriptor));
  expect(await readCustomHarness(path)).toMatchObject(descriptor);
  await writeFile(path, JSON.stringify({ ...descriptor, configPath: '../relative.json' }));
  await expect(readCustomHarness(path)).rejects.toThrow('Invalid custom harness');
});

const writeHookRuntime = async (f: Awaited<ReturnType<typeof fixture>>) => {
  await mkdir(dirname(f.bridge.cliPath), { recursive: true });
  await writeFile(
    f.bridge.cliPath,
    `import { writeFileSync } from 'node:fs';
let raw = ''; for await (const chunk of process.stdin) raw += chunk;
const event = JSON.parse(raw); writeFileSync(${JSON.stringify(f.capture)}, JSON.stringify(event));
if (process.argv.slice(2, 4).join(' ') !== 'hooks run') process.exit(1);
process.exit(event.tool_input.code ?? 0);`,
  );
};

it('awaits generated OpenCode V1/V2 callbacks and propagates a denied tool call', async () => {
  const f = await fixture();
  const agent = agentsFor(f.home).opencode!;
  await writeHookRuntime(f);
  await installHooks(agent, f.bridge, f.checker);
  const { pathToFileURL } = await import('node:url');
  const pluginFile = join(f.home, 'plugin.mjs');
  await writeFile(pluginFile, await readFile(agent.hookConfig!.path, 'utf8'));
  const plugin = (await import(/* @vite-ignore */ pathToFileURL(pluginFile).href)).default;
  const v1 = await plugin.server({ directory: f.home });
  await v1['tool.execute.before'](
    { tool: canonical.tool_name, sessionID: 'session', callID: 'call' },
    { args: canonical.tool_input },
  );
  expect(JSON.parse(await readFile(f.capture, 'utf8'))).toMatchObject({
    ...canonical,
    cwd: f.home,
  });
  let callback = async (_event: unknown) => {
    throw new Error('Hook was not registered.');
  };
  await plugin.setup({
    tool: {
      hook: async (name: string, handler: typeof callback) => {
        expect(name).toBe('execute.before');
        callback = handler;
      },
    },
  });
  await callback({
    sessionID: 'session',
    id: 'call',
    tool: canonical.tool_name,
    input: canonical.tool_input,
  });
  expect(JSON.parse(await readFile(f.capture, 'utf8'))).toEqual(canonical);
  await expect(
    callback({ sessionID: 'session', id: 'call', tool: 'Bash', input: { code: 2 } }),
  ).rejects.toThrow('denied or failed');
});

it.each(['claude', 'codex'] as const)(
  'executes the %s shell hook with quoted paths, JSON stdin, and denial exit status',
  async (format) => {
    const f = await fixture();
    const special = join(f.home, "agent's $(touch injected)");
    const runtimeFixture = {
      ...f,
      bridge: {
        ...f.bridge,
        cliPath: join(special, 'runtime.mjs'),
        configDirectory: join(special, 'config'),
      },
    };
    await writeHookRuntime(runtimeFixture);
    const agent = agentsFor(f.home)[format]!;
    await installHooks(agent, runtimeFixture.bridge, f.checker);
    const config = JSON.parse(await readFile(agent.hookConfig!.path, 'utf8'));
    const command = config.hooks.PreToolUse[0].hooks[0].command as string;
    const invoke = (code: number) =>
      new Promise<number | null>((resolve, reject) => {
        const windowsShell = format === 'claude' ? 'powershell.exe' : 'cmd.exe';
        const executable = process.platform === 'win32' ? windowsShell : '/bin/sh';
        const args =
          process.platform === 'win32' ? windowsArguments(format, command) : ['-c', command];
        const child = spawn(executable, args, { cwd: f.home, stdio: ['pipe', 'ignore', 'ignore'] });
        child.once('error', reject);
        child.once('close', resolve);
        child.stdin.end(JSON.stringify({ ...canonical, tool_input: { code } }));
      });
    expect(await invoke(0)).toBe(0);
    expect(JSON.parse(await readFile(f.capture, 'utf8'))).toEqual({
      ...canonical,
      tool_input: { code: 0 },
    });
    expect(await invoke(2)).toBe(2);
    await expect(readFile(join(f.home, 'injected'))).rejects.toMatchObject({ code: 'ENOENT' });
  },
);

const windowsArguments = (format: string, command: string): string[] =>
  format === 'claude'
    ? ['-NoProfile', '-NonInteractive', '-Command', command]
    : ['/d', '/s', '/c', command];
