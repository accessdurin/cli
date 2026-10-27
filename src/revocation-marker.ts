import { randomUUID } from 'node:crypto';
import { mkdir, rename, rm, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import type { RevocationMarker } from './ports/revocation-marker.js';

const SIGN_OUT_UNFINISHED =
  'Signing out of this durin profile hasn’t finished: the authorization server could not confirm that access was revoked, so MCP calls and durin tools are blocked for this profile. Run durin logout again to finish, or durin logout --force to sign out on this computer without the server.';
const missingFile = (error: unknown): boolean =>
  z.object({ code: z.literal('ENOENT') }).safeParse(error).success;

/** Only the marker's presence matters, so a damaged or unreadable marker still blocks the profile. */
export class FileRevocationMarker implements RevocationMarker {
  readonly path: string;
  private readonly directory: string;
  constructor(directory: string, profileId: string) {
    this.directory = join(directory, 'revocations');
    this.path = join(this.directory, `${cliProfileIdSchema.parse(profileId)}.pending`);
  }
  async pending(): Promise<boolean> {
    return stat(this.path).then(
      () => true,
      (error: unknown) => !missingFile(error),
    );
  }
  async mark(): Promise<void> {
    await mkdir(this.directory, { recursive: true, mode: 0o700 });
    const temporary = `${this.path}.${randomUUID()}.tmp`;
    try {
      await writeFile(temporary, `${JSON.stringify({ markedAt: new Date().toISOString() })}\n`, {
        mode: 0o600,
        flag: 'wx',
      });
      await rename(temporary, this.path);
    } finally {
      await rm(temporary, { force: true });
    }
  }
  async clear(): Promise<void> {
    await rm(this.path, { force: true });
  }
}

export async function assertSignOutFinished(marker: RevocationMarker): Promise<void> {
  if (await marker.pending()) throw new CliError(SIGN_OUT_UNFINISHED);
}
