import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { setTimeout } from 'node:timers/promises';
import { expect, it } from 'vitest';
import { withProfileLock } from './profile-lock.js';

it('serializes same-profile token rotations and releases the lock after a failed operation', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-profile-lock-'));
  try {
    let refresh = 'initial';
    const seen: string[] = [];
    const rotate = () =>
      withProfileLock(directory, 'a'.repeat(64), async () => {
        seen.push(refresh);
        await setTimeout(20);
        refresh += '-rotated';
      });
    await Promise.all([rotate(), rotate()]);
    expect(seen).toEqual(['initial', 'initial-rotated']);
    await expect(
      withProfileLock(directory, 'a'.repeat(64), async () => {
        throw new Error('failed');
      }),
    ).rejects.toThrow('failed');
    expect(await withProfileLock(directory, 'a'.repeat(64), async () => 'released')).toBe(
      'released',
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

it('does not race another process by unlinking an abandoned lock automatically', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'durin-abandoned-lock-'));
  try {
    await mkdir(join(directory, 'locks'));
    await writeFile(
      join(directory, 'locks', `${'a'.repeat(64)}.lock`),
      JSON.stringify({ pid: 2147483647, nonce: '10000000-0000-4000-8000-000000000001' }),
    );
    await expect(withProfileLock(directory, 'a'.repeat(64), async () => 'unsafe')).rejects.toThrow(
      'interrupted',
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
