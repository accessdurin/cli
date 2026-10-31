import { randomUUID } from 'node:crypto';
import { lstat, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import { cliProfileIdSchema } from './contracts/cli-profile.js';
import { atomicWrite } from './profiles.js';
import type { RevocationMarker } from './ports/revocation-marker.js';
import type { CredentialStore } from './ports/credential-store.js';
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

const pendingIndexSchema = z
  .object({
    grants: z.array(
      z.object({ id: z.uuid(), clientId: z.string().min(1), addedAt: z.iso.datetime() }).strict(),
    ),
  })
  .strict();
type IndexEntry = z.infer<typeof pendingIndexSchema>['grants'][number];
const pendingTokensSchema = z
  .object({ accessToken: z.string().min(1), refreshToken: z.string().min(1).optional() })
  .strict();
type StoredGrant = { readonly entry: IndexEntry; readonly grant: UnrevokedGrant | undefined };
const parseJson = (raw: string): unknown => {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
};
const sameGrant =
  (grant: UnrevokedGrant) =>
  (entry: UnrevokedGrant | undefined): boolean =>
    entry?.clientId === grant.clientId &&
    entry.accessToken === grant.accessToken &&
    entry.refreshToken === grant.refreshToken;
const tokensOf = ({ accessToken, refreshToken }: UnrevokedGrant) =>
  JSON.stringify(refreshToken ? { accessToken, refreshToken } : { accessToken });

/**
 * The tokens live in the OS credential store, one entry per grant, like the MCP credentials;
 * the private index on disk holds only ids and timestamps. Callers hold the profile lock. An
 * index that cannot be read is kept as it is rather than overwritten, so no grant is silently
 * forgotten, and a credential store that cannot be used fails like the MCP credentials do.
 */
export class FilePendingRevocations implements PendingRevocations {
  readonly path: string;
  constructor(
    directory: string,
    private readonly profileId: string,
    private readonly store: CredentialStore,
  ) {
    this.path = join(
      directory,
      'revocations',
      `${cliProfileIdSchema.parse(profileId)}.unrevoked.json`,
    );
  }
  async entries(): Promise<readonly UnrevokedGrant[]> {
    return (await this.stored()).flatMap(({ grant }) => (grant ? [grant] : []));
  }
  /** The index entry comes first, so a crash never leaves tokens in the store untracked. */
  async add(grant: UnrevokedGrant): Promise<void> {
    const stored = await this.stored();
    if (stored.some((current) => sameGrant(grant)(current.grant))) return;
    const index = stored.map(({ entry }) => entry);
    const entry = { id: randomUUID(), clientId: grant.clientId, addedAt: new Date().toISOString() };
    await this.writeIndex([...index, entry]);
    await this.store.write(this.key(entry.id), tokensOf(grant)).catch(async (error: unknown) => {
      await this.writeIndex(index).catch(() => {});
      throw error;
    });
  }
  /** Tokens leave the store before their index entry, for the same reason. */
  async replace(grants: readonly UnrevokedGrant[]): Promise<void> {
    const stored = await this.stored();
    const kept = (current: StoredGrant) => grants.some((grant) => sameGrant(grant)(current.grant));
    await Promise.all(stored.filter((entry) => !kept(entry)).map((entry) => this.forget(entry)));
    await this.writeIndex(stored.filter(kept).map(({ entry }) => entry));
  }

  private async stored(): Promise<StoredGrant[]> {
    const index = await this.readIndex();
    return Promise.all(index.map(async (entry) => ({ entry, grant: await this.readGrant(entry) })));
  }
  private async readGrant(entry: IndexEntry): Promise<UnrevokedGrant | undefined> {
    const raw = await this.store.read(this.key(entry.id));
    const tokens = pendingTokensSchema.safeParse(raw === null ? null : parseJson(raw));
    return tokens.success ? { clientId: entry.clientId, ...tokens.data } : undefined;
  }
  /** An entry without readable tokens can never be revoked, so removing it is best effort. */
  private async forget({ entry, grant }: StoredGrant): Promise<void> {
    const removal = this.store.remove(this.key(entry.id));
    await (grant ? removal : removal.catch(() => {}));
  }
  private async readIndex(): Promise<readonly IndexEntry[]> {
    const raw = await this.read();
    if (raw === null) return [];
    const parsed = pendingIndexSchema.safeParse(parseJson(raw));
    if (!parsed.success) throw this.unreadable();
    return parsed.data.grants;
  }
  private async writeIndex(grants: readonly IndexEntry[]): Promise<void> {
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
  private key(id: string): string {
    return `mcp:${this.profileId}:unrevoked:${id}`;
  }
}

export async function assertSignOutFinished(marker: RevocationMarker): Promise<void> {
  if (await marker.pending()) throw new CliError(SIGN_OUT_UNFINISHED);
}
