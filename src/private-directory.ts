import { stat } from 'node:fs/promises';
import { z } from 'zod';
import { CliError } from './cli-options.js';

const missing = (error: unknown): null => {
  if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return null;
  throw error;
};
/** Agents execute the runtime stored here, so other local users must not control it. */
export async function assertPrivateDirectory(directory: string): Promise<void> {
  if (typeof process.getuid !== 'function') return;
  const info = await stat(directory).catch(missing);
  if (!info) return;
  const unsafe = [!info.isDirectory(), info.uid !== process.getuid(), (info.mode & 0o022) !== 0];
  if (unsafe.some(Boolean))
    throw new CliError(
      `The Durin configuration directory ${directory} must be owned by you and not writable by other users. Run chmod 700 on it, or choose another --config-dir.`,
    );
}
