import { readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { CliError, type CliOptions } from './cli-options.js';
import { FileProfiles } from './profiles.js';
import { NativeCredentialStore } from './credentials.js';
import type { CredentialStore } from './ports/credential-store.js';
import { retryPendingRevocations, signOutMcp } from './logout.js';
import { withProfileLock } from './profile-lock.js';
import { FileRevocationMarker } from './revocation-marker.js';
import { printJson } from './tool-commands.js';
import type { CliProfile } from './contracts/cli-profile.js';

/** Pinned production revocation endpoint. Never taken from a saved profile. */
const PRODUCTION_MCP_ISSUER = 'https://auth.getdurin.com';
const PRODUCTION_REVOKE_ENDPOINT = 'https://auth.getdurin.com/revoke';
const comparableIssuer = (issuer: string): string =>
  issuer.endsWith('/') ? issuer.slice(0, -1) : issuer;
const trustedProductionIssuer = (issuer: string): boolean =>
  comparableIssuer(issuer) === PRODUCTION_MCP_ISSUER;
const UNTRUSTED_SESSION =
  "The server session was not revoked because its issuer isn't trusted; it will expire on its own.";

export async function profileCommand(options: CliOptions): Promise<void> {
  const profiles = new FileProfiles(options.directory);
  const action = options.positionals[1];
  if (action === 'list') return printJson(await profiles.list());
  if (action !== 'use') throw new CliError('Use durin profiles list or durin profiles use <id>.');
  const id = options.positionals[2];
  if (!id) throw new CliError('Provide a profile ID from durin profiles list.');
  await profiles.activate(id);
  printJson({ activeProfile: id });
}
const logoutLoaded = async (options: CliOptions, profile: CliProfile): Promise<void> => {
  const store = new NativeCredentialStore();
  await retryPendingRevocations(options.directory, profile, store);
  const { remote, local } = await withProfileLock(options.directory, profile.id, () =>
    signOutMcp({
      profile,
      store,
      marker: new FileRevocationMarker(options.directory, profile.id),
      request: fetch,
      force: options.values.force ?? false,
    }),
  );
  printJson({
    profile: profile.id,
    localMcpGrant: local,
    remoteMcpGrant: remote,
    detail:
      local === 'retained'
        ? 'The authorization server could not confirm that access was revoked, so sign-out hasn’t finished. Until it does, MCP calls and durin tools are blocked for this profile. Run durin logout again to retry, or durin logout --force to sign out on this computer anyway and lift the block.'
        : 'Agent configuration is retained. Run durin to sign in again. Other organization MCP grants are unchanged.',
  });
  if (remote === 'failed') process.exitCode = 1;
};
const loadForLogout = async (
  options: CliOptions,
): Promise<{ profile?: CliProfile; reason: string }> => {
  try {
    return {
      reason: '',
      profile: await new FileProfiles(options.directory).load(options.values.profile),
    };
  } catch (error) {
    if (!(error instanceof CliError)) throw error;
    return { reason: error.message };
  }
};
export async function logoutCommand(options: CliOptions): Promise<void> {
  const loaded = await loadForLogout(options);
  if (loaded.profile) return logoutLoaded(options, loaded.profile);
  await logoutRefused(options, loaded.reason);
}
const selectedProfileId = async (options: CliOptions): Promise<string> => {
  if (options.values.profile) return options.values.profile;
  return (await readFile(join(options.directory, 'active'), 'utf8')).trim();
};
const record = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' ? { ...value } : {};
const stringField = (value: Record<string, unknown>, key: string): string => {
  const field = value[key];
  return typeof field === 'string' ? field : '';
};
const savedMcpIssuer = async (directory: string, id: string): Promise<string> => {
  const text = await readFile(join(directory, 'profiles', `${id}.json`), 'utf8').catch(() => '');
  try {
    return stringField(record(JSON.parse(text)), 'mcpIssuer');
  } catch {
    return '';
  }
};
const storedRefreshToken = async (store: CredentialStore, id: string): Promise<string> => {
  const text = await store.read(`mcp:${id}:tokens`).catch(() => null);
  if (!text) return '';
  try {
    const parsed = record(JSON.parse(text));
    return stringField(parsed, 'refresh_token') || stringField(parsed, 'access_token');
  } catch {
    return '';
  }
};
const revokeAtProduction = async (token: string): Promise<boolean> => {
  const response = await fetch(PRODUCTION_REVOKE_ENDPOINT, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({ token, token_type_hint: 'refresh_token', client_id: 'durin' }),
  }).catch(() => undefined);
  await response?.body?.cancel();
  return response?.ok === true;
};
const clearRefusedProfile = async (
  directory: string,
  id: string,
  store: CredentialStore,
): Promise<void> => {
  await store.remove(`mcp:${id}:tokens`).catch(() => undefined);
  await store.remove(`mcp:${id}:client`).catch(() => undefined);
  await rm(join(directory, 'profiles', `${id}.json`), { force: true });
  const active = join(directory, 'active');
  const current = await readFile(active, 'utf8').catch(() => '');
  if (current.trim() === id) await rm(active, { force: true });
};
const refusedDetail = (id: string, revoked: boolean | undefined): string => {
  const removed = `Removed local credentials for ${id}.`;
  if (revoked === undefined) return `${removed} ${UNTRUSTED_SESSION}`;
  if (revoked) return `${removed} The server session was revoked.`;
  return `${removed} The server revoke failed; the session may still be valid until it expires.`;
};
const logoutRefused = async (options: CliOptions, reason: string): Promise<void> => {
  const id = await selectedProfileId(options);
  const issuer = await savedMcpIssuer(options.directory, id);
  const store = new NativeCredentialStore();
  const token = await storedRefreshToken(store, id);
  const trusted = trustedProductionIssuer(issuer) && token.length > 0;
  const revoked = trusted ? await revokeAtProduction(token) : undefined;
  await clearRefusedProfile(options.directory, id, store);
  if (revoked === false) process.exitCode = 1;
  printJson({ profile: id, removed: true, reason, detail: refusedDetail(id, revoked) });
};
