import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readdir, readFile, rename, rm, rmdir, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import {
  MCP_REPORT_LOCAL_TOOL_CALL_TOOL,
  localToolCallSchema,
  type LocalToolCall,
} from './contracts/local-tool-calls.js';
import type { CliMcpAccess } from './ports/cli-mcp.js';

/**
 * Calls wait here until a background flush delivers them, so hooks never block on the
 * network. Each profile-lock hold covers one connection and at most `chunk` calls, so the
 * Durin bridge, which needs the same lock, is never kept waiting for a whole spool.
 */
export const SPOOL_LIMITS = {
  files: 200,
  ageMs: 7 * 86_400_000,
  lockMs: 120_000,
  temporaryMs: 3_600_000,
  chunk: 5,
  perFlush: 50,
  attempts: 5,
  backoffMs: 60_000,
  maxBackoffMs: 3_600_000,
} as const;
export const spoolDirectory = (directory: string, profileId: string) =>
  join(directory, 'reporting', 'spool', createHash('sha256').update(profileId).digest('hex'));

const FILE = /^([0-9a-f-]{36})(?:\.(\d))?\.json$/u;
type Listed = Readonly<{ path: string; name: string; at: number }>;
type Pending = Listed & Readonly<{ attempt: number }>;
type Clock = () => number;

export async function spoolCall(spool: string, call: LocalToolCall, now: Clock = Date.now) {
  await mkdir(spool, { recursive: true, mode: 0o700 });
  const temporary = join(spool, `.${randomUUID()}.tmp`);
  await writeFile(temporary, JSON.stringify(localToolCallSchema.parse(call)), {
    mode: 0o600,
    flag: 'wx',
  });
  await rename(temporary, join(spool, `${call.reportId}.json`));
  await trimSpool(spool, now);
}
async function entry(spool: string, name: string): Promise<Listed | null> {
  const info = await stat(join(spool, name)).catch(() => null);
  return info ? { path: join(spool, name), name, at: info.mtimeMs } : null;
}
async function listed(spool: string): Promise<Listed[]> {
  const names = await readdir(spool).catch(() => []);
  const entries = await Promise.all(names.map((name) => entry(spool, name)));
  return entries.filter((item) => item !== null);
}
const pendingFiles = (entries: readonly Listed[]): Pending[] =>
  entries
    .flatMap((item) => {
      const match = FILE.exec(item.name);
      return match ? [{ ...item, attempt: Number(match[2] ?? 0) }] : [];
    })
    .toSorted((a, b) => a.at - b.at);
const staleTemporary = (item: Listed, now: Clock) =>
  item.name.endsWith('.tmp') && item.at < now() - SPOOL_LIMITS.temporaryMs;
/** Keeps the newest calls within budget, drops old ones and leftovers of interrupted writes. */
async function trimSpool(spool: string, now: Clock): Promise<Pending[]> {
  const entries = await listed(spool);
  const files = pendingFiles(entries);
  const excess = files.length - SPOOL_LIMITS.files;
  const expired = files.filter(
    (file, index) => index < excess || file.at < now() - SPOOL_LIMITS.ageMs,
  );
  const dropped = [...expired, ...entries.filter((item) => staleTemporary(item, now))];
  await Promise.all(dropped.map((item) => rm(item.path, { force: true })));
  return files.filter((file) => !expired.includes(file));
}

const created = (lock: string) =>
  mkdir(lock).then(
    () => true,
    () => false,
  );
const stale = async (lock: string) => {
  const info = await stat(lock).catch(() => null);
  return info !== null && Date.now() - info.mtimeMs >= SPOOL_LIMITS.lockMs;
};
/** Takes the flush lock, replacing it once if a crashed flush left it behind. */
async function acquire(lock: string): Promise<boolean> {
  if (await created(lock)) return true;
  if (!(await stale(lock))) return false;
  await rm(lock, { recursive: true, force: true }).catch(() => {});
  return created(lock);
}

const safeJson = (text: string): unknown => {
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return null;
  }
};
const backoffSchema = z.object({ until: z.number(), failures: z.number().int().min(0) });
const backoffPath = (spool: string) => join(spool, '.backoff.json');
async function readBackoff(spool: string) {
  const raw = await readFile(backoffPath(spool), 'utf8').catch(() => 'null');
  return backoffSchema.safeParse(safeJson(raw)).data ?? { until: 0, failures: 0 };
}
async function recordOutcome(spool: string, failed: boolean, now: Clock): Promise<void> {
  if (!failed) return rm(backoffPath(spool), { force: true });
  const failures = (await readBackoff(spool)).failures + 1;
  const delay = Math.min(SPOOL_LIMITS.backoffMs * 2 ** (failures - 1), SPOOL_LIMITS.maxBackoffMs);
  await writeFile(backoffPath(spool), JSON.stringify({ until: now() + delay, failures }), {
    mode: 0o600,
  });
}

type Sender = Pick<CliMcpAccess, 'callTool'>;
type Delivery = 'sent' | 'rejected' | 'dropped';
/** A rejected call (for example from a gateway without the tool yet) is retried a few times. */
async function rejected(file: Pending): Promise<Delivery> {
  const next = file.path.replace(/(?:\.\d)?\.json$/u, `.${file.attempt + 1}.json`);
  await (file.attempt + 1 >= SPOOL_LIMITS.attempts
    ? rm(file.path, { force: true })
    : rename(file.path, next));
  return 'rejected';
}
async function deliver(client: Sender, file: Pending): Promise<Delivery> {
  const parsed = localToolCallSchema.safeParse(
    safeJson(await readFile(file.path, 'utf8').catch(() => '')),
  );
  if (!parsed.success) return rm(file.path, { force: true }).then(() => 'dropped' as const);
  const result = await client.callTool({
    name: MCP_REPORT_LOCAL_TOOL_CALL_TOOL,
    arguments: { call: parsed.data },
    idempotencyKey: parsed.data.reportId,
  });
  if (result.isError) return rejected(file);
  await rm(file.path, { force: true });
  return 'sent';
}
const sequential = async (client: Sender, files: readonly Pending[]) =>
  files.reduce<Promise<Delivery[]>>(
    async (previous, file) => [...(await previous), await deliver(client, file)],
    Promise.resolve([]),
  );

export type Connect = <T>(work: (client: Sender) => Promise<T>) => Promise<T>;
type Round = Readonly<{ sent: number; failed: boolean }>;
/** Sends the spool in chunks until it is empty, the per-flush cap is reached, or a call is rejected. */
async function rounds(spool: string, connect: Connect, now: Clock, sent: number): Promise<Round> {
  const files = (await trimSpool(spool, now)).slice(0, SPOOL_LIMITS.chunk);
  if (files.length === 0 || sent >= SPOOL_LIMITS.perFlush) return { sent, failed: false };
  const results = await connect((client) => sequential(client, files));
  const total = sent + results.filter((result) => result === 'sent').length;
  return results.includes('rejected')
    ? { sent: total, failed: true }
    : rounds(spool, connect, now, total);
}

/**
 * Delivers spooled calls. Calls queued while a flush runs are picked up by that flush,
 * because it re-lists the spool before each chunk. After a failure, flushes back off
 * exponentially up to an hour. A thrown error (offline, signed out) is re-thrown after
 * the backoff is recorded; the remaining calls stay for a later flush.
 */
export async function flushSpool(
  spool: string,
  connect: Connect,
  now: Clock = Date.now,
): Promise<number> {
  if ((await readBackoff(spool)).until > now()) return 0;
  const lock = join(spool, '.flush.lock');
  if (!(await acquire(lock))) return 0;
  try {
    const round = await rounds(spool, connect, now, 0).catch(async (error: unknown) => {
      await recordOutcome(spool, true, now);
      throw error;
    });
    await recordOutcome(spool, round.failed, now);
    return round.sent;
  } finally {
    await rmdir(lock).catch(() => {});
  }
}
