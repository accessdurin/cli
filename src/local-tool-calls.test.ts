import { mkdtemp, mkdir, readdir, readFile, rm, utimes, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Readable } from 'node:stream';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { agentsFor } from './agents.js';
import { installReporting, reportingAvailable, updatedReportConfig } from './install-reporting.js';
import { entryTransport, localServer, loopbackUrl } from './local-mcp-servers.js';
import { endpointReportingMarker, localToolCall, reportHook } from './report-hook.js';
import { flushSpool, spoolCall, spoolDirectory, SPOOL_LIMITS } from './report-spool.js';
import { mcpTargets, observedMcpCall } from './tool-call-event.js';
import type { LocalToolCall } from './contracts/local-tool-calls.js';

const temporary: string[] = [];
afterEach(async () => {
  vi.restoreAllMocks();
  await Promise.all(temporary.splice(0).map((path) => rm(path, { recursive: true, force: true })));
});
const home = async () => {
  const path = await mkdtemp(join(tmpdir(), 'durin-reporting-'));
  temporary.push(path);
  return path;
};
const PROFILE = 'b'.repeat(64);
const bridgeEntry = {
  command: process.execPath,
  args: ['/opt/durin/cli.mjs', 'mcp', 'serve', '--profile', PROFILE, '--config-dir', '/x'],
};
const claudeEvent = (changes: Record<string, unknown> = {}) => ({
  hook_event_name: 'PostToolUse',
  session_id: 'session-1',
  prompt_id: 'prompt-9',
  tool_use_id: 'toolu_01',
  tool_name: 'mcp__filesystem__read_file',
  tool_input: { path: '/home/dev/notes.md' },
  tool_response: { content: [{ type: 'text', text: 'PRIVATE RESULT' }] },
  cwd: '/home/dev/project',
  ...changes,
});

describe('post-tool events', () => {
  it.each([
    [
      'claude',
      claudeEvent(),
      { targets: [{ server: 'filesystem', tool: 'read_file' }], turnId: 'prompt-9' },
    ],
    [
      'claude',
      claudeEvent({ hook_event_name: 'PostToolUseFailure' }),
      { outcome: 'failed', toolCallId: 'toolu_01' },
    ],
    [
      'codex',
      { ...claudeEvent(), turn_id: 'turn-3', tool_response: { isError: true } },
      { turnId: 'turn-3', outcome: 'failed' },
    ],
    [
      'cursor',
      {
        conversation_id: 'conversation',
        generation_id: 'generation',
        tool_name: 'MCP:search',
        tool_input: '{"query":"durin"}',
        mcp_server_name: 'docs',
        result_json: '{"content":[]}',
        workspace_roots: ['/repo'],
      },
      { targets: [{ server: 'docs', tool: 'search' }], input: { query: 'durin' }, cwd: '/repo' },
    ],
    [
      'windsurf',
      {
        agent_action_name: 'post_mcp_tool_use',
        trajectory_id: 'trajectory',
        execution_id: 'execution',
        tool_info: { mcp_server_name: 'db', mcp_tool_name: 'query', mcp_tool_arguments: { q: 1 } },
      },
      {
        targets: [{ server: 'db', tool: 'query' }],
        outcome: 'unknown',
        sessionId: 'trajectory',
        turnId: 'execution',
      },
    ],
  ] as const)('reads %s MCP calls', (format, raw, expected) => {
    expect(observedMcpCall(format, raw)).toMatchObject(expected);
  });
  it('ignores built-in tools and offers every split of a name containing __', () => {
    expect(observedMcpCall('claude', claudeEvent({ tool_name: 'Bash' }))).toBeNull();
    expect(mcpTargets('mcp__a__b__c')).toEqual([
      { server: 'a', tool: 'b__c' },
      { server: 'a__b', tool: 'c' },
    ]);
  });
});

describe('local server classification', () => {
  it.each([
    [{ command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem'] }, 'stdio'],
    [{ command: ['uvx', 'mcp-server-git'] }, 'stdio'],
    [{ type: 'http', url: 'http://127.0.0.1:3845/mcp' }, 'loopback_http'],
    [{ serverUrl: 'http://localhost:8080/sse' }, 'loopback_http'],
    [{ url: 'http://[::1]:9000/mcp' }, 'loopback_http'],
    [{ url: 'https://mcp.example.com/mcp' }, null],
    [{ url: 'http://127.0.0.1.example.com/mcp' }, null],
    [{ command: 'npx', disabled: true }, null],
    [bridgeEntry, null],
    [{ command: 'durin', args: ['mcp', 'serve', `--profile=${PROFILE}`] }, null],
    [{}, null],
  ])('classifies %j as %s', (entry, expected) => {
    expect(entryTransport(entry)).toBe(expected);
  });
  it('rejects malformed URLs', () => expect(loopbackUrl('not a url')).toBe(false));
  it('follows Claude Code precedence from a subfolder and matches normalized names', async () => {
    const root = await home();
    const project = join(root, 'project');
    await mkdir(join(project, 'packages', 'api'), { recursive: true });
    const agent = agentsFor(root).claude!;
    await writeFile(
      agent.configPath,
      JSON.stringify({
        mcpServers: {
          filesystem: { command: 'npx' },
          shadowed: { command: 'npx' },
          'my.files': { command: 'node' },
          a__b: { command: 'node' },
          'durin-bbbbbbbbbbbb': bridgeEntry,
        },
        projects: { [project]: { mcpServers: { scoped: { url: 'http://localhost:1/mcp' } } } },
      }),
    );
    await writeFile(
      join(project, '.mcp.json'),
      JSON.stringify({
        mcpServers: { team: { command: 'node' }, shadowed: { url: 'https://remote.example/mcp' } },
      }),
    );
    const lookup = (name: string) =>
      localServer({
        format: 'claude',
        agent,
        targets: mcpTargets(name),
        cwd: join(project, 'packages', 'api'),
      });
    expect(await lookup('mcp__filesystem__read')).toEqual({
      transport: 'stdio',
      server: 'filesystem',
      tool: 'read',
    });
    expect(await lookup('mcp__scoped__q')).toMatchObject({ transport: 'loopback_http' });
    expect(await lookup('mcp__team__q')).toMatchObject({ transport: 'stdio' });
    expect(await lookup('mcp__shadowed__q')).toBeNull();
    expect(await lookup('mcp__my_files__read')).toMatchObject({ server: 'my.files' });
    expect(await lookup('mcp__a__b__run')).toMatchObject({ server: 'a__b', tool: 'run' });
    expect(await lookup('mcp__durin-bbbbbbbbbbbb__durin_call_tool')).toBeNull();
    expect(await lookup('mcp__plugin_tools_db__q')).toBeNull();
  });
});

const STDIO = { transport: 'stdio', server: 'filesystem', tool: 'read_file' } as const;
describe('reported calls', () => {
  const observed = observedMcpCall('claude', claudeEvent())!;
  it('derives a stable report ID from the agent call and never sends results', () => {
    const first = localToolCall('claude', observed, STDIO)!;
    const second = localToolCall('claude', observed, STDIO)!;
    expect(first.reportId).toBe(second.reportId);
    expect(first.reportId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-/u);
    expect(JSON.parse(first.argumentsJson)).toEqual({ path: '/home/dev/notes.md' });
    expect(JSON.stringify(first)).not.toContain('PRIVATE RESULT');
  });
  it('truncates large arguments, wraps non-objects and drops invalid identifiers', () => {
    const large = localToolCall(
      'claude',
      { ...observed, input: { blob: 'x'.repeat(20_000) } },
      STDIO,
    );
    expect(large).toMatchObject({ argumentsJson: '{}', argumentsTruncated: true });
    const wrapped = localToolCall(
      'claude',
      { ...observed, input: ['a'], sessionId: 'has space' },
      STDIO,
    );
    expect(wrapped).toMatchObject({ argumentsJson: '{"value":["a"]}', sessionId: null });
    expect(localToolCall('claude', observed, { ...STDIO, server: 'quote"server' })).toBeNull();
  });
});

async function reportingFixture() {
  const root = await home();
  const agents = agentsFor(root);
  const bridge = {
    nodePath: process.execPath,
    cliPath: join(root, 'runtime.mjs'),
    profileId: PROFILE,
    configDirectory: join(root, 'durin'),
  };
  await writeFile(
    agents.claude!.configPath,
    JSON.stringify({ mcpServers: { filesystem: { command: 'npx' } } }),
  );
  await installReporting(agents.claude!, bridge);
  const flush = vi.fn<(input: { cliPath: string; directory: string; profileId: string }) => void>();
  const run = (event: unknown, endpoint = false) =>
    reportHook(
      {
        directory: bridge.configDirectory,
        harness: 'claude',
        cliPath: bridge.cliPath,
        stdin: Readable.from([JSON.stringify(event)]),
      },
      { endpointPresent: async () => endpoint, flush },
    );
  const spool = spoolDirectory(bridge.configDirectory, PROFILE);
  const spooled = async () =>
    (await readdir(spool).catch(() => [])).filter((name) => name.endsWith('.json'));
  return { root, agents, bridge, flush, run, spool, spooled };
}
describe('report hook', () => {
  it('queues local calls once and starts a background flush', async () => {
    const fixture = await reportingFixture();
    expect(await fixture.run(claudeEvent())).toBe('queued');
    expect(await fixture.run(claudeEvent())).toBe('queued');
    const files = await fixture.spooled();
    expect(files).toHaveLength(1);
    const call = JSON.parse(
      await readFile(join(fixture.spool, files[0]!), 'utf8'),
    ) as LocalToolCall;
    expect(call).toMatchObject({
      reporter: 'cli',
      agent: 'claude',
      mcpServer: 'filesystem',
      transport: 'stdio',
    });
    expect(fixture.flush).toHaveBeenCalledWith({
      cliPath: fixture.bridge.cliPath,
      directory: fixture.bridge.configDirectory,
      profileId: PROFILE,
    });
  });
  it('stays quiet when Endpoint covers the harness, the server is not local, or the tool is built in', async () => {
    const fixture = await reportingFixture();
    expect(await fixture.run(claudeEvent(), true)).toBe('endpoint');
    expect(await fixture.run(claudeEvent({ tool_name: 'mcp__remote__search' }))).toBe('not_local');
    expect(await fixture.run(claudeEvent({ tool_name: 'Read' }))).toBe('ignored');
    expect(await fixture.spooled()).toEqual([]);
    expect(fixture.flush).not.toHaveBeenCalled();
  });
  it('defers to Endpoint only while its helper is wired in and it is signed in', async () => {
    const fixture = await reportingFixture();
    const helper = '/Applications/Durin Endpoint Client.app/Contents/Helpers/observer-preflight';
    const covers = vi.spyOn(await import('./hook-checker.js'), 'endpointChecker');
    covers.mockResolvedValue({ command: helper, args: [], timeoutMs: 1000 });
    const hooks = fixture.agents.claude!.hookConfig!.path;
    const original = await readFile(hooks, 'utf8');
    const run = () =>
      reportHook(
        {
          directory: fixture.bridge.configDirectory,
          harness: 'claude',
          cliPath: fixture.bridge.cliPath,
          stdin: Readable.from([JSON.stringify(claudeEvent())]),
        },
        { flush: fixture.flush, home: fixture.root },
      );
    expect(await run()).toBe('queued');
    const marker = endpointReportingMarker(fixture.root);
    await mkdir(join(marker, '..'), { recursive: true });
    await writeFile(marker, '{"signedIn":true}');
    expect(await run()).toBe('queued');
    await writeFile(
      hooks,
      original.replace(
        '"hooks": {',
        `"hooks": { "PreToolUse": [{ "matcher": ".*", "hooks": [{ "type": "command", "command": "'${helper}' --client claude" }] }],`,
      ),
    );
    expect(await run()).toBe('endpoint');
    const stale = new Date(Date.now() - 3 * 3_600_000);
    await utimes(marker, stale, stale);
    expect(await run()).toBe('queued');
  });
  it('does nothing until reporting is installed for the harness', async () => {
    const root = await home();
    const result = await reportHook(
      { directory: root, harness: 'codex', cliPath: '/x', stdin: Readable.from(['{}']) },
      { endpointPresent: async () => false, flush: vi.fn<() => void>() },
    );
    expect(result).toBe('disabled');
  });
});

describe('spool', () => {
  const call = (index: number): LocalToolCall => ({
    ...localToolCall(
      'claude',
      { ...observedMcpCall('claude', claudeEvent())!, toolCallId: `call-${index}` },
      STDIO,
    )!,
  });
  const files = async (spool: string) =>
    (await readdir(spool)).filter((name) => name.endsWith('.json') && !name.startsWith('.'));
  const accepting = (sent: unknown[], connections: { count: number }) =>
    (async (work) => {
      connections.count += 1;
      return work({
        callTool: async (input) => {
          sent.push(input);
          return { content: [], isError: false };
        },
      });
    }) as Parameters<typeof flushSpool>[1];
  it('delivers in short sessions, backs off after a failure, and keeps calls for later', async () => {
    const spool = join(await home(), 'spool');
    await Promise.all(Array.from({ length: 12 }, (_, index) => spoolCall(spool, call(index))));
    let now = Date.now();
    const clock = () => now;
    const failing = vi.fn<() => Promise<never>>(async () => {
      throw new Error('offline');
    });
    await expect(flushSpool(spool, async () => failing(), clock)).rejects.toThrow('offline');
    expect(await files(spool)).toHaveLength(12);
    const sent: unknown[] = [];
    const connections = { count: 0 };
    expect(await flushSpool(spool, accepting(sent, connections), clock)).toBe(0);
    now += SPOOL_LIMITS.backoffMs + 1;
    expect(await flushSpool(spool, accepting(sent, connections), clock)).toBe(12);
    expect(connections.count).toBe(3);
    expect(sent[0]).toMatchObject({
      name: 'durin_report_local_tool_call',
      arguments: { call: { reporter: 'cli' } },
      idempotencyKey: expect.stringMatching(/^[0-9a-f-]{36}$/u),
    });
    expect(await files(spool)).toEqual([]);
  });
  it('picks up calls queued during a flush and drops unreadable files', async () => {
    const spool = join(await home(), 'spool');
    await spoolCall(spool, call(1));
    await writeFile(join(spool, '00000000-0000-4000-8000-000000000000.json'), '');
    const sent: unknown[] = [];
    let queued = false;
    const result = await flushSpool(spool, async (work) =>
      work({
        callTool: async (input) => {
          sent.push(input);
          if (!queued) await spoolCall(spool, call(2));
          queued = true;
          return { content: [], isError: false };
        },
      }),
    );
    expect(result).toBe(2);
    expect(await files(spool)).toEqual([]);
  });
  it('retries a rejected call a few times, then drops it', async () => {
    const spool = join(await home(), 'spool');
    await spoolCall(spool, call(1));
    let now = Date.now();
    const rejecting = (async (work) =>
      work({ callTool: async () => ({ content: [], isError: true }) })) as Parameters<
      typeof flushSpool
    >[1];
    const attempts = Array.from({ length: SPOOL_LIMITS.attempts }, () => 0);
    await attempts.reduce(async (previous) => {
      await previous;
      now += SPOOL_LIMITS.maxBackoffMs + 1;
      await flushSpool(spool, rejecting, () => now);
    }, Promise.resolve());
    expect(await files(spool)).toEqual([]);
  });
  it('keeps only the newest calls within the file budget', async () => {
    const spool = join(await home(), 'spool');
    await Promise.all(
      Array.from({ length: SPOOL_LIMITS.files + 5 }, (_, index) => spoolCall(spool, call(index))),
    );
    expect(
      (await readdir(spool)).filter((name) => name.endsWith('.json')).length,
    ).toBeLessThanOrEqual(SPOOL_LIMITS.files);
  });
});

describe('hook configuration', () => {
  const command = "'/node' '/cli.mjs' 'hooks' 'report'";
  it('adds Claude Code handlers once and keeps existing hooks', () => {
    const raw = JSON.stringify({
      hooks: { PostToolUse: [{ matcher: 'Bash', hooks: [{ type: 'command', command: 'lint' }] }] },
    });
    const once = updatedReportConfig(raw, 'claude', command);
    expect(updatedReportConfig(once, 'claude', command)).toBe(once);
    const hooks = JSON.parse(once).hooks;
    expect(hooks.PostToolUse).toHaveLength(2);
    expect(hooks.PostToolUse[1]).toEqual({
      matcher: 'mcp__.*',
      hooks: [{ type: 'command', command, async: true, timeout: 30 }],
    });
    expect(hooks.PostToolUseFailure).toHaveLength(1);
  });
  it.each([
    ['cursor', { version: 1, hooks: { afterMCPExecution: [{ command, timeout: 10 }] } }],
    ['windsurf', { hooks: { post_mcp_tool_use: [{ command, show_output: false }] } }],
    [
      'codex',
      {
        hooks: {
          PostToolUse: [
            { matcher: 'mcp__.*', hooks: [{ type: 'command', command, async: true, timeout: 30 }] },
          ],
        },
      },
    ],
  ] as const)('writes the %s handler', (format, expected) => {
    expect(JSON.parse(updatedReportConfig('{}', format, command))).toEqual(expected);
  });
  it('replaces the handler for a previous CLI path instead of adding another', () => {
    const moved = "'/node' '/new/cli.mjs' 'hooks' 'report'";
    const updated = updatedReportConfig(
      updatedReportConfig('{}', 'claude', command),
      'claude',
      moved,
      command,
    );
    const hooks = JSON.parse(updated).hooks;
    expect(hooks.PostToolUse).toHaveLength(1);
    expect(hooks.PostToolUse[0].hooks[0].command).toBe(moved);
    expect(
      JSON.parse(
        updatedReportConfig(updatedReportConfig('{}', 'cursor', command), 'cursor', moved, command),
      ).hooks.afterMCPExecution,
    ).toEqual([{ command: moved, timeout: 10 }]);
  });
  it('refuses to edit a harness whose hooks are disabled', () => {
    expect(() => updatedReportConfig('{"disableAllHooks":true}', 'claude', command)).toThrow(
      'disabled',
    );
  });
  it('refuses harnesses without a reporting adapter', async () => {
    const root = await home();
    await expect(
      installReporting(agentsFor(root).gemini!, {
        nodePath: process.execPath,
        cliPath: '/cli.mjs',
        profileId: PROFILE,
        configDirectory: root,
      }),
    ).rejects.toThrow('no local tool-call reporting adapter');
    expect(await reportingAvailable(agentsFor(root).gemini!)).toBe(false);
    const claude = agentsFor(root).claude!;
    expect(await reportingAvailable(claude)).toBe(true);
    await mkdir(join(claude.hookConfig!.path, '..'), { recursive: true });
    await writeFile(claude.hookConfig!.path, '{"disableAllHooks":true}');
    expect(await reportingAvailable(claude)).toBe(false);
  });
});
