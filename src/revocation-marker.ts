import { lstat, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import { atomicWrite } from './profiles.js';
import type { RevocationMarker } from './ports/revocation-marker.js';
import type { PendingRevocations, UnrevokedGrant } from './ports/pending-revocations.js';

export const SIGN_OUT_UNFINISHED =
  'Signing out of this durin profile hasn’t finished: the authorization server could not confirm that access was revoked, so MCP calls and durin tools are blocked for this profile. Run durin logout again to finish, or durin logout --force to sign out on this computer without the server.';
const missingFile = (error: unknown): boolean =>
  z.object({ code: z.literal('ENOENT') }).safeParse(error).success;

/**
 * Only the marker's presence matters, so a damaged or unreadable marker still blocks the profile,
 * and so does a symlink in its place, even one whose target is missing.
 */
export class FileRevocationMarker implements RevocationMarker {
  readonly path: string;
  constructor(directory: string, profileId: string) {
    this.path = join(directory, 'revocations', `${cliProfileIdSchema.parse(profileId)}.pending`);
  }
  async pending(): Promise<boolean> {
    return lstat(this.path).then(
      () => true,
      (error: unknown) => !missingFile(error),
    );
  }
  async mark(): Promise<void> {
    await atomicWrite(this.path, `${JSON.stringify({ markedAt: new Date().toISOString() })}\n`);
  }
  async clear(): Promise<void> {
    await rm(this.path, { force: true });
  }
}

const unrevokedGrantSchema = z
  .object({
    clientId: z.string().min(1),
    accessToken: z.string().min(1),
    refreshToken: z.string().min(1).optional(),
  })
  .strict();
const pendingListSchema = z.object({ grants: z.array(unrevokedGrantSchema) }).strict();
const parseJson = (raw: string): unknown => {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
};
const sameGrant =
  (grant: UnrevokedGrant) =>
  (entry: UnrevokedGrant): boolean =>
    entry.clientId === grant.clientId &&
    entry.accessToken === grant.accessToken &&
    entry.refreshToken === grant.refreshToken;

/**
 * Holds live tokens, so it is private to this user. Callers hold the profile lock. A list that
 * cannot be read is kept as it is rather than overwritten, so no grant is silently forgotten.
 */
export class FilePendingRevocations implements PendingRevocations {
  readonly path: string;
  constructor(directory: string, profileId: string) {
    this.path = join(
      directory,
      'revocations',
      `${cliProfileIdSchema.parse(profileId)}.unrevoked.json`,
    );
  }
  async entries(): Promise<readonly UnrevokedGrant[]> {
    const raw = await this.read();
    if (raw === null) return [];
    const parsed = pendingListSchema.safeParse(parseJson(raw));
    if (!parsed.success) throw this.unreadable();
    return parsed.data.grants;
  }
  async add(grant: UnrevokedGrant): Promise<void> {
    const grants = await this.entries();
    if (!grants.some(sameGrant(grant))) await this.replace([...grants, grant]);
  }
  async replace(grants: readonly UnrevokedGrant[]): Promise<void> {
    if (grants.length === 0) return rm(this.path, { force: true });
    await atomicWrite(this.path, `${JSON.stringify({ grants })}\n`);
  }
  /** Anything but a regular file here, a dangling symlink included, is unreadable, not absent. */
  private async read(): Promise<string | null> {
    const info = await lstat(this.path).catch((error: unknown) => {
      if (missingFile(error)) return null;
      throw error;
    });
    if (!info) return null;
    if (!info.isFile()) throw this.unreadable();
    return readFile(this.path, 'utf8');
  }
  private unreadable(): CliError {
    return new CliError(
      `durin can’t read its list of earlier sign-ins that still need to be revoked (${this.path}). Move that file aside, then run durin again.`,
    );
  }
}

export async function assertSignOutFinished(marker: RevocationMarker): Promise<void> {
  if (await marker.pending()) throw new CliError(SIGN_OUT_UNFINISHED);
}
