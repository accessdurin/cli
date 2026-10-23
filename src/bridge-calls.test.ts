import { mkdtemp, readdir, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it, vi } from 'vitest';
import { JSONRPCRequestSchema } from '@modelcontextprotocol/core';
import type { JSONRPCMessage } from '@modelcontextprotocol/client';
import { CALL_RETENTION_MS, FileCallLedger, BridgeCalls } from './bridge-calls.js';
import type { PendingCliCall } from './ports/cli-call-ledger.js';

const request = {
  jsonrpc: '2.0' as const,
  id: 1,
  method: 'tools/call',
  params: { name: 'github__create_issue', arguments: { title: 'Fixture' } },
};
const pendingApproval = { status: 'approval_required', approvalId: 'approval_one' };
it.each([
  { format: 'object', structuredContent: pendingApproval },
  { format: 'legacy wrapper', structuredContent: { result: pendingApproval } },
])(
  'resumes the same approval across bridge restarts with $format content',
  async ({ structuredContent }) => {
    const directory = await mkdtemp(join(tmpdir(), 'durin-call-ledger-'));
    try {
      const first = new BridgeCalls(new FileCallLedger(directory, 'a'.repeat(64)));
      const sent = await first.outgoing(request);
      const control = JSONRPCRequestSchema.parse(sent).params!._meta;
      expect(control).toMatchObject({ 'io.durin/control': { idempotencyKey: expect.any(String) } });
      const resumed = new BridgeCalls(new FileCallLedger(directory, 'a'.repeat(64)));
      expect(
        JSONRPCRequestSchema.parse(await resumed.outgoing({ ...request, id: 2 })).params!._meta,
      ).toEqual(control);
      const approval = {
        jsonrpc: '2.0' as const,
        id: 2,
        result: {
          content: [],
          isError: true,
          structuredContent,
        },
      };
      const send = vi.fn<(message: JSONRPCMessage) => Promise<void>>(async () => {});
      await resumed.deliver(approval, send);
      expect(send).toHaveBeenCalledWith({
        ...approval,
        result: { ...approval.result, _meta: control },
      });
      expect(
        JSONRPCRequestSchema.parse(await resumed.outgoing({ ...request, id: 3 })).params!._meta,
      ).toMatchObject({ 'io.durin/control': { approvalId: 'approval_one' } });
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  },
);

it.each([{ failedDelivery: false }, { failedDelivery: true }])(
  'retains an operation only until its successful response is delivered: $failedDelivery',
  async ({ failedDelivery }) => {
    const values = new Map<string, PendingCliCall>();
    const bridge = new BridgeCalls({
      read: async (key) => values.get(key) ?? null,
      save: async (key, value) => {
        values.set(key, value);
      },
      remove: async (key) => {
        values.delete(key);
      },
    });
    const prepared = JSONRPCRequestSchema.parse(await bridge.outgoing(request));
    const deliver = bridge.deliver(
      { jsonrpc: '2.0', id: 1, result: { content: [{ type: 'text', text: 'Done' }] } },
      async () => {
        if (failedDelivery) throw new Error('stdio closed');
      },
    );
    const error = await deliver.then(
      () => null,
      (failure: Error) => failure.message,
    );
    expect(error).toBe(failedDelivery ? 'stdio closed' : null);
    const resumed = JSONRPCRequestSchema.parse(await bridge.outgoing({ ...request, id: 2 }));
    expect(JSON.stringify(resumed.params?._meta) === JSON.stringify(prepared.params?._meta)).toBe(
      failedDelivery,
    );
  },
);

const memoryLedger = () => {
  const values = new Map<string, PendingCliCall>();
  return {
    read: async (key: string) => values.get(key) ?? null,
    save: async (key: string, value: PendingCliCall) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
};
const controlOf = (message: JSONRPCMessage) =>
  JSONRPCRequestSchema.parse(message).params!._meta!['io.durin/control'];
const ignoreDelivery = async () => {};

it('clears an operation after any successful result, including one larger than the CLI prints', async () => {
  const bridge = new BridgeCalls(memoryLedger());
  const first = controlOf(await bridge.outgoing(request));
  await bridge.deliver(
    { jsonrpc: '2.0', id: 1, result: { content: [{ type: 'text', text: 'x'.repeat(20_000) }] } },
    ignoreDelivery,
  );
  expect(controlOf(await bridge.outgoing({ ...request, id: 2 }))).not.toEqual(first);
});

it.each([{ isError: true }, {}])(
  'keeps an approval request ($isError) and uses the approval only once',
  async (flag) => {
    const bridge = new BridgeCalls(memoryLedger());
    const { idempotencyKey } = controlOf(await bridge.outgoing(request)) as PendingCliCall;
    await bridge.deliver(
      {
        jsonrpc: '2.0',
        id: 1,
        result: { content: [], ...flag, structuredContent: pendingApproval },
      },
      ignoreDelivery,
    );
    expect(controlOf(await bridge.outgoing({ ...request, id: 2 }))).toEqual({
      idempotencyKey,
      approvalId: 'approval_one',
    });
    await bridge.deliver(
      {
        jsonrpc: '2.0',
        id: 2,
        result: { content: [{ type: 'text', text: 'upstream failed' }], isError: true },
      },
      ignoreDelivery,
    );
    expect(controlOf(await bridge.outgoing({ ...request, id: 3 }))).toEqual({ idempotencyKey });
  },
);

it('stops resuming an operation a day after its last state change, even across retries', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-call-ledger-'));
  try {
    let now = Date.now();
    const ledger = new FileCallLedger(directory, 'a'.repeat(64), () => now);
    const bridge = new BridgeCalls(ledger);
    const first = controlOf(await bridge.outgoing(request));
    const [entry] = await readdir(join(directory, 'calls', 'a'.repeat(64)));
    const path = join(directory, 'calls', 'a'.repeat(64), entry!);
    const saved = (await stat(path)).mtimeMs;
    now = saved + CALL_RETENTION_MS - 1;
    expect(controlOf(await bridge.outgoing({ ...request, id: 2 }))).toEqual(first);
    expect((await stat(path)).mtimeMs).toBe(saved);
    now = saved + CALL_RETENTION_MS;
    expect(controlOf(await bridge.outgoing({ ...request, id: 3 }))).not.toEqual(first);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

it.each([
  {
    name: 'input_required',
    result: { resultType: 'input_required', inputRequests: {}, requestState: 'state-one' },
  },
  { name: 'task', result: { task: { taskId: 'task-one', status: 'working' } } },
])('keeps the same operation across a non-final $name result', async ({ result }) => {
  const bridge = new BridgeCalls(memoryLedger());
  const first = controlOf(await bridge.outgoing(request));
  await bridge.deliver({ jsonrpc: '2.0', id: 1, result }, ignoreDelivery);
  expect(controlOf(await bridge.outgoing({ ...request, id: 2 }))).toEqual(first);
});

it('never stores or replays an approval ID that is unsafe on a command line', async () => {
  const bridge = new BridgeCalls(memoryLedger());
  const first = controlOf(await bridge.outgoing(request));
  await bridge.deliver(
    {
      jsonrpc: '2.0',
      id: 1,
      result: {
        content: [],
        structuredContent: { status: 'approval_required', approvalId: "x'; touch /tmp/pwned; '" },
      },
    },
    ignoreDelivery,
  );
  expect(controlOf(await bridge.outgoing({ ...request, id: 2 }))).toEqual(first);
});
