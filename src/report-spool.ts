import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readdir, readFile, rename, rm, rmdir, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import {
  MCP_REPORT_LOCAL_TOOL_CALL_TOOL,
  localToolCallSchema,
  type LocalToolCall,
} from './contracts/local-tool-calls.js';
import type { CliMcpAccess } from './ports/cli-mcp.js';

/** Calls wait here until a background flush delivers them, so hooks never block on the network. */
export const SPOOL_LIMITS = { files: 200, ageMs: 7 * 86_400_000, lockMs: 120_000 } as const;
export const spoolDirectory = (directory: string, profileId: string) =>
  join(directory, 'reporting', 'spool', createHash('sha256').update(profileId).digest('hex'));

export async function spoolCall(spool: string, call: LocalToolCall): Promise<void> {
  await mkdir(spool, { recursive: true, mode: 0o700 });
  const temporary = join(spool, `.${randomUUID()}.tmp`);
  await writeFile(temporary, JSON.stringify(localToolCallSchema.parse(call)), {
    mode: 0o600,
    flag: 'wx',
  });
  await rename(temporary, join(spool, `${call.reportId}.json`));
  await trimSpool(spool);
}
async function pending(spool: string): Promise<{ path: string; at: number }[]> {
  const names = await readdir(spool).catch(() => []);
  const files = names.filter((name) => /^[0-9a-f-]{36}\.json$/u.test(name));
  const entries = await Promise.all(
    files.map(async (name) => {
      const path = join(spool, name);
      const info = await stat(path).catch(() => null);
      return info ? [{ path, at: info.mtimeMs }] : [];
    }),
  );
  return entries.flat().toSorted((a, b) => a.at - b.at);
}
/** Keeps the newest calls within the file budget and drops ones too old to matter. */
async function trimSpool(spool: string): Promise<void> {
  const files = await pending(spool);
  const cutoff = Date.now() - SPOOL_LIMITS.ageMs;
  const excess = files.length - SPOOL_LIMITS.files;
  const dropped = files.filter((file, index) => index < excess || file.at < cutoff);
  await Promise.all(dropped.map((file) => rm(file.path, { force: true })));
}

async function acquire(lock: string): Promise<boolean> {
  try {
    await mkdir(lock);
    return true;
  } catch {
    const info = await stat(lock).catch(() => null);
    if (!info || Date.now() - info.mtimeMs < SPOOL_LIMITS.lockMs) return false;
    await rmdir(lock).catch(() => {});
    return acquire(lock);
  }
}
async function deliver(client: Pick<CliMcpAccess, 'callTool'>, path: string): Promise<void> {
  const parsed = localToolCallSchema.safeParse(
    JSON.parse(await readFile(path, 'utf8').catch(() => 'null')),
  );
  // A call the gateway answered, even with a rejection, would fail the same way again.
  if (parsed.success)
    await client.callTool({
      name: MCP_REPORT_LOCAL_TOOL_CALL_TOOL,
      arguments: { call: parsed.data },
      idempotencyKey: parsed.data.reportId,
    });
  await rm(path, { force: true });
}

/**
 * Sends every spooled call over one MCP session. A thrown error (offline, signed
 * out) stops the flush and leaves the remaining calls for the next one.
 */
export async function flushSpool(
  spool: string,
  connect: <T>(work: (client: Pick<CliMcpAccess, 'callTool'>) => Promise<T>) => Promise<T>,
): Promise<number> {
  await trimSpool(spool);
  const files = await pending(spool);
  if (files.length === 0) return 0;
  const lock = join(spool, '.flush.lock');
  if (!(await acquire(lock))) return 0;
  try {
    return await connect(async (client) => {
      await files.reduce(async (previous, file) => {
        await previous;
        await deliver(client, file.path);
      }, Promise.resolve());
      return files.length;
    });
  } finally {
    await rmdir(lock).catch(() => {});
  }
}
