import { discoverAuthorizationServerMetadata } from '@modelcontextprotocol/client';
import { readdir, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import type { CliOptions } from './cli-options.js';
import { NativeCredentialStore } from './credentials.js';
import type { CredentialStore } from './ports/credential-store.js';
import { withProfileLock } from './profile-lock.js';
import { secureFetch } from './safe-fetch.js';
import { printJson } from './tool-commands.js';

/** Pinned production hosts. Never taken from a saved profile. */
const PRODUCTION_APP = 'https://app.getdurin.com';
const PRODUCTION_RESOURCE = 'https://mcp.getdurin.com';
const PRODUCTION_ISSUER = 'https://auth.getdurin.com';
const PRODUCTION_HOST = 'auth.getdurin.com';
const UNTRUSTED_SESSION =
  "The server session was not revoked because its issuer isn't trusted; it will expire on its own.";
const NOT_REVOKED = 'The server session was not revoked; it will expire on its own.';
/** Bound at call time so a test can replace `fetch` before logout runs. */
const productionFetch = (): typeof fetch =>
  secureFetch({ hosts: [PRODUCTION_HOST], development: false });

const record = (value: unknown): Record<string, unknown> =>
  value !== null && typeof value === 'object' ? { ...value } : {};
const stringField = (value: Record<string, unknown>, key: string): string => {
  const field = value[key];
  return typeof field === 'string' ? field : '';
};
const stripOneSlash = (issuer: string): string =>
  issuer.endsWith('/') ? issuer.slice(0, -1) : issuer;
/** Exactly the production issuer, or that issuer plus one trailing slash. */
const productionIssuer = (issuer: string): boolean => stripOneSlash(issuer) === PRODUCTION_ISSUER;
const resourceOrigin = (resource: string): string =>
  URL.canParse(resource) ? new URL(resource).origin : '';
const productionApp = (raw: Record<string, unknown>): boolean =>
  stringField(raw, 'appOrigin') === PRODUCTION_APP;
const productionResource = (raw: Record<string, unknown>): boolean =>
  resourceOrigin(stringField(raw, 'resource')) === PRODUCTION_RESOURCE;
const trustedProduction = (raw: Record<string, unknown>): boolean =>
  [
    productionApp(raw),
    productionResource(raw),
    productionIssuer(stringField(raw, 'mcpIssuer')),
  ].every(Boolean);

const readRawProfile = async (directory: string, id: string): Promise<Record<string, unknown>> => {
  const text = await readFile(join(directory, 'profiles', `${id}.json`), 'utf8').catch(() => '');
  try {
    return record(JSON.parse(text));
  } catch {
    return {};
  }
};
const selectedProfileId = async (options: CliOptions): Promise<string> => {
  if (options.values.profile) return options.values.profile;
  return (await readFile(join(options.directory, 'active'), 'utf8')).trim();
};

type HeldToken = { readonly token: string; readonly hint: 'refresh_token' | 'access_token' };
const heldToken = (parsed: Record<string, unknown>): HeldToken => {
  const refresh = stringField(parsed, 'refresh_token');
  if (refresh) return { token: refresh, hint: 'refresh_token' };
  return { token: stringField(parsed, 'access_token'), hint: 'access_token' };
};
const storedToken = async (store: CredentialStore, id: string): Promise<HeldToken> => {
  const text = await store.read(`mcp:${id}:tokens`).catch(() => null);
  if (!text) return { token: '', hint: 'access_token' };
  try {
    return heldToken(record(JSON.parse(text)));
  } catch {
    return { token: '', hint: 'access_token' };
  }
};
const storedClientId = async (store: CredentialStore, id: string): Promise<string> => {
  const text = await store.read(`mcp:${id}:client`).catch(() => null);
  if (!text) return '';
  try {
    return stringField(record(JSON.parse(text)), 'client_id');
  } catch {
    return '';
  }
};

/** Only an endpoint the production metadata actually names, and only on the pinned host. */
const pinnedRevocation = (metadata: unknown): string => {
  const endpoint = stringField(record(metadata), 'revocation_endpoint');
  if (!URL.canParse(endpoint)) return '';
  const url = new URL(endpoint);
  return url.host === PRODUCTION_HOST ? url.href : '';
};
const advertisedRevocation = async (): Promise<string> => {
  const metadata = await discoverAuthorizationServerMetadata(PRODUCTION_ISSUER, {
    fetchFn: productionFetch(),
  }).catch(() => undefined);
  return pinnedRevocation(metadata);
};
const postRevoke = async (
  endpoint: string,
  held: HeldToken,
  clientId: string,
): Promise<boolean> => {
  const response = await productionFetch()(endpoint, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      token: held.token,
      token_type_hint: held.hint,
      client_id: clientId,
    }),
  }).catch(() => undefined);
  await response?.body?.cancel();
  return response?.ok === true;
};
const revokeProduction = async (store: CredentialStore, id: string): Promise<boolean> => {
  const held = await storedToken(store, id);
  const clientId = await storedClientId(store, id);
  if (!held.token || !clientId) return false;
  const endpoint = await advertisedRevocation();
  if (!endpoint) return false;
  return postRevoke(endpoint, held, clientId);
};

const grantIds = (raw: string): readonly string[] => {
  try {
    const grants = record(JSON.parse(raw)).grants;
    if (!Array.isArray(grants)) return [];
    return grants.flatMap((entry) => {
      const id = stringField(record(entry), 'id');
      return id ? [id] : [];
    });
  } catch {
    return [];
  }
};
const removeKey = async (store: CredentialStore, key: string): Promise<void> => {
  await store.remove(key).catch(() => undefined);
};
const clearUnrevokedKeys = async (
  path: string,
  id: string,
  store: CredentialStore,
): Promise<void> => {
  const raw = await readFile(path, 'utf8').catch(() => '');
  await Promise.all(
    grantIds(raw).map((grantId) => removeKey(store, `mcp:${id}:unrevoked:${grantId}`)),
  );
};
const removeRevocation =
  (revocations: string, id: string, store: CredentialStore) =>
  async (name: string): Promise<void> => {
    const path = join(revocations, name);
    if (name === `${id}.unrevoked.json`) await clearUnrevokedKeys(path, id, store);
    await rm(path, { force: true });
  };
const clearRevocations = async (
  directory: string,
  id: string,
  store: CredentialStore,
): Promise<void> => {
  const revocations = join(directory, 'revocations');
  const names = await readdir(revocations).catch(() => [] as string[]);
  await Promise.all(
    names.filter((name) => name.startsWith(id)).map(removeRevocation(revocations, id, store)),
  );
};
const clearActive = async (directory: string, id: string): Promise<void> => {
  const active = join(directory, 'active');
  const current = await readFile(active, 'utf8').catch(() => '');
  if (current.trim() === id) await rm(active, { force: true });
};
const clearRefusedProfile = async (
  directory: string,
  id: string,
  store: CredentialStore,
): Promise<void> => {
  await removeKey(store, `mcp:${id}:tokens`);
  await removeKey(store, `mcp:${id}:client`);
  await clearRevocations(directory, id, store);
  await rm(join(directory, 'profiles', `${id}.json`), { force: true });
  await clearActive(directory, id);
};
const refusedDetail = (id: string, revoked: boolean | undefined): string => {
  const removed = `Removed local credentials for ${id}.`;
  if (revoked === undefined) return `${removed} ${UNTRUSTED_SESSION}`;
  if (revoked) return `${removed} The server session was revoked.`;
  return `${removed} ${NOT_REVOKED}`;
};
const finishRefusedLogout = async (
  directory: string,
  id: string,
  reason: string,
): Promise<void> => {
  const raw = await readRawProfile(directory, id);
  const store = new NativeCredentialStore();
  const revoked = trustedProduction(raw) ? await revokeProduction(store, id) : undefined;
  await clearRefusedProfile(directory, id, store);
  printJson({ profile: id, removed: true, reason, detail: refusedDetail(id, revoked) });
};

/** Deletes a profile that can no longer be loaded. Revokes only a real production grant. */
export async function logoutRefusedProfile(options: CliOptions, reason: string): Promise<void> {
  const id = await selectedProfileId(options);
  await withProfileLock(options.directory, id, () =>
    finishRefusedLogout(options.directory, id, reason),
  );
}
