import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import {
  isJSONRPCRequest,
  isJSONRPCResultResponse,
  type JSONRPCMessage,
  type JSONRPCRequest,
  type JSONRPCResultResponse,
} from '@modelcontextprotocol/client';
import type { CliCallLedger, PendingCliCall } from './ports/cli-call-ledger.js';
import { canonicalJson as canonicalArguments } from './canonical-json.js';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import { mcpToolResultSchema } from './contracts/mcp-result.js';

const controlKey = 'io.durin/control';
const pendingSchema = z
  .object({ idempotencyKey: z.string().min(1), approvalId: z.string().optional() })
  .strict();
const approvalSchema = z.object({
  status: z.literal('approval_required'),
  approvalId: z.string().min(1),
});
const missingFile = (error: unknown): null => {
  if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return null;
  throw error;
};

/** Only operation identifiers and a hash of the request are persisted, never tool arguments. */
export class FileCallLedger implements CliCallLedger {
  private readonly directory: string;
  constructor(directory: string, profileId: string) {
    this.directory = join(directory, 'calls', cliProfileIdSchema.parse(profileId));
  }
  async read(fingerprint: string): Promise<PendingCliCall | null> {
    const raw = await readFile(this.path(fingerprint), 'utf8').catch(missingFile);
    return raw === null ? null : pendingSchema.parse(JSON.parse(raw));
  }
  async save(fingerprint: string, call: PendingCliCall): Promise<void> {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const path = this.path(fingerprint),
      temporary = `${path}.${randomUUID()}`;
    try {
      await writeFile(temporary, JSON.stringify(pendingSchema.parse(call)), {
        mode: 0o600,
        flag: 'wx',
      });
      await rename(temporary, path);
    } finally {
      await rm(temporary, { force: true });
    }
  }
  async remove(fingerprint: string): Promise<void> {
    await rm(this.path(fingerprint), { force: true });
  }
  private path(fingerprint: string): string {
    return join(this.directory, `${cliProfileIdSchema.parse(fingerprint)}.json`);
  }
}

type TrackedCall = { fingerprint: string; control: PendingCliCall };
export class BridgeCalls {
  private readonly requests = new Map<string | number, TrackedCall>();
  constructor(private readonly ledger: CliCallLedger) {}

  async outgoing(message: JSONRPCMessage): Promise<JSONRPCMessage> {
    if (!nativeCall(message)) return message;
    return this.prepareCall(message);
  }
  private async prepareCall(message: JSONRPCRequest): Promise<JSONRPCMessage> {
    const params = message.params ?? {};
    if (hasControl(params)) return message;
    const fingerprint = callFingerprint(params);
    const control = (await this.ledger.read(fingerprint)) ?? {
      idempotencyKey: `cli_${randomUUID()}`,
    };
    await this.ledger.save(fingerprint, control);
    this.requests.set(message.id, { fingerprint, control });
    return { ...message, params: { ...params, _meta: { ...params._meta, [controlKey]: control } } };
  }

  async deliver(
    message: JSONRPCMessage,
    send: (message: JSONRPCMessage) => Promise<void>,
  ): Promise<void> {
    if (!isJSONRPCResultResponse(message)) return send(message);
    const tracked = this.requests.get(message.id);
    if (!tracked) return send(message);
    return this.deliverCall(message, tracked, send);
  }
  private async deliverCall(
    message: JSONRPCResultResponse,
    tracked: TrackedCall,
    send: (message: JSONRPCMessage) => Promise<void>,
  ): Promise<void> {
    const approval = approvalSchema.safeParse(message.result.structuredContent);
    if (approval.success)
      await this.ledger.save(tracked.fingerprint, {
        ...tracked.control,
        approvalId: approval.data.approvalId,
      });
    await send({
      ...message,
      result: {
        ...message.result,
        _meta: { ...message.result._meta, [controlKey]: tracked.control },
      },
    });
    this.requests.delete(message.id);
    if (completedCall(message.result)) await this.ledger.remove(tracked.fingerprint);
  }
}

const completedCall = (result: unknown): boolean => {
  const parsed = mcpToolResultSchema.safeParse(result);
  return parsed.success && parsed.data.isError !== true;
};
const nativeCall = (message: JSONRPCMessage): message is JSONRPCRequest =>
  isJSONRPCRequest(message) && message.method === 'tools/call';
const callFingerprint = (params: NonNullable<JSONRPCRequest['params']>): string =>
  createHash('sha256')
    .update(canonicalArguments({ name: params.name, arguments: params.arguments ?? {} }))
    .digest('hex');

const hasControl = (
  params: NonNullable<Extract<JSONRPCMessage, { method: string; id: unknown }>['params']>,
): boolean => {
  const legacy = z.object({ durinIdempotencyKey: z.string() }).safeParse(params.arguments);
  return Object.hasOwn(params._meta ?? {}, controlKey) || legacy.success;
};
