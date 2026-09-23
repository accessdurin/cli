import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { expect, it } from 'vitest';
import { JSONRPCRequestSchema } from '@modelcontextprotocol/core';
import { FileCallLedger, BridgeCalls } from './bridge-calls.js';
import type { PendingCliCall } from './ports/cli-call-ledger.js';

const request = {
  jsonrpc: '2.0' as const,
  id: 1,
  method: 'tools/call',
  params: { name: 'github__create_issue', arguments: { title: 'Fixture' } },
};
it('preserves an uncertain call across bridge restarts and resumes the same approval without generating another operation', async () => {
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
        structuredContent: { status: 'approval_required', approvalId: 'approval_one' },
      },
    };
    await resumed.deliver(approval, async () => {});
    expect(
      JSONRPCRequestSchema.parse(await resumed.outgoing({ ...request, id: 3 })).params!._meta,
    ).toMatchObject({ 'io.durin/control': { approvalId: 'approval_one' } });
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

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
