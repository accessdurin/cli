import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
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
import { cliIdentifierSchema } from './contracts/cli-mcp.js';

const controlKey = 'io.durin/control';
/** An unresolved operation is resumed only this long after its last state change. */
export const CALL_RETENTION_MS = 24 * 60 * 60 * 1000;
const pendingSchema = z
  .object({ idempotencyKey: z.string().min(1), approvalId: z.string().optional() })
  .strict();
const approvalSchema = z.object({
  status: z.literal('approval_required'),
  approvalId: z.string().min(1),
});
const approvalContentSchema = z.union([
  approvalSchema,
  z.object({ result: approvalSchema }).transform(({ result }) => result),
]);
const missingFile = (error: unknown): null => {
  if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return null;
  throw error;
};

/** Only operation identifiers and a hash of the request are persisted, never tool arguments. */
export class FileCallLedger implements CliCallLedger {
  private readonly directory: string;
  constructor(
    directory: string,
    profileId: string,
    private readonly now: () => number = Date.now,
  ) {
    this.directory = join(directory, 'calls', cliProfileIdSchema.parse(profileId));
  }
  async read(fingerprint: string): Promise<PendingCliCall | null> {
    const path = this.path(fingerprint);
    const saved = await stat(path).catch(missingFile);
    if (saved === null) return null;
    if (this.now() - saved.mtimeMs >= CALL_RETENTION_MS) {
      await rm(path, { force: true });
      return null;
    }
    const raw = await readFile(path, 'utf8').catch(missingFile);
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
    const control = await this.operation(fingerprint);
    this.requests.set(message.id, { fingerprint, control });
    return { ...message, params: { ...params, _meta: { ...params._meta, [controlKey]: control } } };
  }
  /** A new operation is recorded before it is sent; resuming one never extends its retention. */
  private async operation(fingerprint: string): Promise<PendingCliCall> {
    const pending = await this.ledger.read(fingerprint);
    if (pending) return pending;
    const control = { idempotencyKey: `cli_${randomUUID()}` };
    await this.ledger.save(fingerprint, control);
    return control;
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
    const resolved = await this.recordOutcome(message.result, tracked);
    await send({
      ...message,
      result: {
        ...message.result,
        _meta: { ...message.result._meta, [controlKey]: tracked.control },
      },
    });
    this.requests.delete(message.id);
    if (resolved) await this.ledger.remove(tracked.fingerprint);
  }
  /** Persists approval state before delivery and reports whether the operation resolved. */
  private async recordOutcome(
    result: JSONRPCResultResponse['result'],
    tracked: TrackedCall,
  ): Promise<boolean> {
    const approval = approvalContentSchema.safeParse(result.structuredContent);
    if (approval.success) {
      await this.ledger.save(
        tracked.fingerprint,
        withApproval(tracked.control, approval.data.approvalId),
      );
      return false;
    }
    if (finalSuccess(result)) return true;
    if (result.isError === true) await this.consumeApproval(tracked);
    return false;
  }
  /** An approval covers one attempt: a failed approved call keeps its key but not its approval. */
  private async consumeApproval({ fingerprint, control }: TrackedCall): Promise<void> {
    if (control.approvalId)
      await this.ledger.save(fingerprint, { idempotencyKey: control.idempotencyKey });
  }
}

/** Agents pass approval IDs on command lines, so only shell-inert identifiers are resumed. */
const withApproval = (control: PendingCliCall, approvalId: string): PendingCliCall =>
  cliIdentifierSchema.safeParse(approvalId).success
    ? { ...control, approvalId }
    : { idempotencyKey: control.idempotencyKey };
/** Multi-round-trip (`input_required`) and task results are not the operation's outcome. */
const finalSuccess = (result: JSONRPCResultResponse['result']): boolean =>
  [
    Array.isArray(result.content),
    (result.resultType ?? 'complete') === 'complete',
    result.task === undefined,
    result.isError !== true,
  ].every(Boolean);
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
