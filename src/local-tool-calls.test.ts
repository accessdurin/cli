import { mkdtemp, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Readable } from 'node:stream';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { agentsFor } from './agents.js';
import { installReporting, updatedReportConfig } from './install-reporting.js';
import { entryTransport, localServerTransport, loopbackUrl } from './local-mcp-servers.js';
import { localToolCall, reportHook } from './report-hook.js';
import { flushSpool, spoolCall, spoolDirectory, SPOOL_LIMITS } from './report-spool.js';
import { observedMcpCall } from './tool-call-event.js';
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
    ['claude', claudeEvent(), { server: 'filesystem', tool: 'read_file', turnId: 'prompt-9' }],
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
      { server: 'docs', tool: 'search', input: { query: 'durin' }, cwd: '/repo' },
    ],
    [
      'windsurf',
      {
        agent_action_name: 'post_mcp_tool_use',
        trajectory_id: 'trajectory',
        execution_id: 'execution',
        tool_info: { mcp_server_name: 'db', mcp_tool_name: 'query', mcp_tool_arguments: { q: 1 } },
      },
      { server: 'db', tool: 'query', sessionId: 'trajectory', turnId: 'execution' },
    ],
  ] as const)('reads %s MCP calls', (format, raw, expected) => {
    expect(observedMcpCall(format, raw)).toMatchObject(expected);
  });
  it('ignores built-in tools', () => {
    expect(observedMcpCall('claude', claudeEvent({ tool_name: 'Bash' }))).toBeNull();
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
    [{}, null],
  ])('classifies %j as %s', (entry, expected) => {
    expect(entryTransport(entry)).toBe(expected);
  });
  it('rejects malformed URLs', () => expect(loopbackUrl('not a url')).toBe(false));
  it('finds user, Claude local-scope and project servers, and skips unknown ones', async () => {
    const root = await home();
    const project = join(root, 'project');
    await mkdir(project);
    const agent = agentsFor(root).claude!;
    await writeFile(
      agent.configPath,
      JSON.stringify({
        mcpServers: { filesystem: { command: 'npx' }, 'durin-bbbbbbbbbbbb': bridgeEntry },
        projects: { [project]: { mcpServers: { scoped: { url: 'http://localhost:1/mcp' } } } },
      }),
    );
    await writeFile(
      join(project, '.mcp.json'),
      JSON.stringify({ mcpServers: { team: { command: 'node', args: ['server.js'] } } }),
    );
    const transport = (server: string) =>
      localServerTransport({ format: 'claude', agent, server, cwd: project });
    expect(await transport('filesystem')).toBe('stdio');
    expect(await transport('scoped')).toBe('loopback_http');
    expect(await transport('team')).toBe('stdio');
    expect(await transport('durin-bbbbbbbbbbbb')).toBeNull();
    expect(await transport('plugin_tools_db')).toBeNull();
  });
});

describe('reported calls', () => {
  const observed = observedMcpCall('claude', claudeEvent())!;
  it('derives a stable report ID from the agent call and never sends results', () => {
    const first = localToolCall('claude', observed, 'stdio')!;
    const second = localToolCall('claude', observed, 'stdio')!;
    expect(first.reportId).toBe(second.reportId);
    expect(first.reportId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-/u);
    expect(JSON.parse(first.argumentsJson)).toEqual({ path: '/home/dev/notes.md' });
    expect(JSON.stringify(first)).not.toContain('PRIVATE RESULT');
  });
  it('truncates large arguments, wraps non-objects and drops invalid identifiers', () => {
    const large = localToolCall(
      'claude',
      { ...observed, input: { blob: 'x'.repeat(20_000) } },
      'stdio',
    );
    expect(large).toMatchObject({ argumentsJson: '{}', argumentsTruncated: true });
    const wrapped = localToolCall(
      'claude',
      { ...observed, input: ['a'], sessionId: 'has space' },
      'stdio',
    );
    expect(wrapped).toMatchObject({ argumentsJson: '{"value":["a"]}', sessionId: null });
    expect(localToolCall('claude', { ...observed, server: 'quote"server' }, 'stdio')).toBeNull();
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
  it('defers to Endpoint only when its helper is wired into the harness hooks', async () => {
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
        { flush: fixture.flush },
      );
    expect(await run()).toBe('queued');
    await writeFile(
      hooks,
      original.replace(
        '"hooks": {',
        `"hooks": { "PreToolUse": [{ "matcher": ".*", "hooks": [{ "type": "command", "command": "'${helper}' --client claude" }] }],`,
      ),
    );
    expect(await run()).toBe('endpoint');
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
      'stdio',
    )!,
  });
  it('delivers each call once over one session and keeps calls after a failure', async () => {
    const spool = join(await home(), 'spool');
    await Promise.all([1, 2, 3].map((index) => spoolCall(spool, call(index))));
    const sent: unknown[] = [];
    const failing = vi.fn<() => Promise<never>>(async () => {
      throw new Error('offline');
    });
    await expect(flushSpool(spool, async () => failing())).rejects.toThrow('offline');
    expect(await readdir(spool)).toHaveLength(3);
    const delivered = await flushSpool(spool, async (work) =>
      work({
        callTool: async (input) => {
          sent.push(input);
          return { content: [], isError: false };
        },
      }),
    );
    expect(delivered).toBe(3);
    expect(sent).toHaveLength(3);
    expect(sent[0]).toMatchObject({
      name: 'durin_report_local_tool_call',
      arguments: { call: { reporter: 'cli' } },
      idempotencyKey: expect.stringMatching(/^[0-9a-f-]{36}$/u),
    });
    expect((await readdir(spool)).filter((name) => name.endsWith('.json'))).toEqual([]);
    expect(await flushSpool(spool, async () => failing())).toBe(0);
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
  });
});
