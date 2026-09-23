import { randomUUID } from 'node:crypto';
import { mkdir, open, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { z } from 'zod';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import { CliError } from './cli-options.js';

const ownerSchema = z.object({ pid: z.number().int().positive(), nonce: z.uuid() }).strict();
const errorCode = (error: unknown, code: string): boolean =>
  z.object({ code: z.literal(code) }).safeParse(error).success;
async function acquire(path: string, owner: string): Promise<boolean> {
  const handle = await open(path, 'wx', 0o600).catch((error: unknown) => {
    if (errorCode(error, 'EEXIST')) return null;
    throw error;
  });
  if (!handle) return false;
  try {
    await handle.writeFile(owner);
    return true;
  } finally {
    await handle.close();
  }
}
const processIsGone = (pid: number): boolean => {
  try {
    process.kill(pid, 0);
    return false;
  } catch (error) {
    return errorCode(error, 'ESRCH');
  }
};
async function assertLockOwner(path: string): Promise<void> {
  const raw = await readFile(path, 'utf8').catch(() => '');
  const parsed = ownerSchema.safeParse(parseOwner(raw));
  if (!parsed.success) return;
  if (processIsGone(parsed.data.pid))
    throw new CliError(
      `A previous Durin process was interrupted. With Durin and its bridges stopped, remove the abandoned lock ${path}, then retry with the same idempotency key.`,
    );
}
const parseOwner = (raw: string): unknown => {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
};
async function removeOwned(path: string, owner: string): Promise<void> {
  const current = await readFile(path, 'utf8').catch(() => null);
  if (current === owner) await rm(path, { force: true });
}
async function waitForLock(path: string, owner: string, deadline: number): Promise<void> {
  if (await acquire(path, owner)) return;
  await assertLockOwner(path);
  if (Date.now() >= deadline)
    throw new CliError(
      'This profile is busy in another Durin process. Finish that request, then retry with the same idempotency key.',
    );
  await setTimeout(100);
  return waitForLock(path, owner, deadline);
}
export async function withProfileLock<T>(
  directory: string,
  profileId: string,
  work: () => Promise<T>,
): Promise<T> {
  const locks = join(directory, 'locks');
  await mkdir(locks, { recursive: true, mode: 0o700 });
  const path = join(locks, `${cliProfileIdSchema.parse(profileId)}.lock`);
  const owner = JSON.stringify({ pid: process.pid, nonce: randomUUID() });
  await waitForLock(path, owner, Date.now() + 90_000);
  try {
    return await work();
  } finally {
    await removeOwned(path, owner);
  }
}
