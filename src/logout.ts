import { discoverAuthorizationServerMetadata } from '@modelcontextprotocol/client';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import type { CliProfile } from './contracts/cli-profile.js';
import type { CredentialStore } from './ports/credential-store.js';
import type { RevocationMarker } from './ports/revocation-marker.js';
import type { PendingRevocations, UnrevokedGrant } from './ports/pending-revocations.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { withProfileLock } from './profile-lock.js';
import { FilePendingRevocations } from './revocation-marker.js';
import { secureFetch } from './safe-fetch.js';

type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly marker: RevocationMarker;
  readonly request: typeof fetch;
  readonly force?: boolean;
};
const revocationSchema = z.object({ revocation_endpoint: z.url().optional() });
export type RevocationStatus = 'revoked' | 'unavailable' | 'failed';
export type SignOutResult = {
  readonly remote: RevocationStatus;
  readonly local: 'cleared' | 'retained';
};
/** The authorization server was unreachable or busy, so revocation may succeed later. */
class TransientRevocationError extends Error {}
const retryableStatus = (status: number): boolean => status === 429 || status >= 500;
const redirected = (error: unknown): boolean =>
  error instanceof TypeError &&
  error.cause instanceof Error &&
  error.cause.message === 'unexpected redirect';
/** Without an HTTP answer the server may still honour the grant; a refused redirect is an answer. */
const transientNetworkFailure = (error: unknown): boolean =>
  !(error instanceof CliError) && !redirected(error);
const transientFailures =
  (request: typeof fetch): typeof fetch =>
  async (input, init) => {
    const response = await request(input, init).catch((error: unknown) => {
      if (!transientNetworkFailure(error)) throw error;
      throw new TransientRevocationError('MCP authorization server unreachable.', { cause: error });
    });
    if (!retryableStatus(response.status)) return response;
    await response.body?.cancel();
    throw new TransientRevocationError(`MCP authorization server returned ${response.status}.`);
  };

/** A 2xx body that is not metadata may come from a portal or proxy rather than the server. */
const unparseable = (error: unknown): boolean =>
  error instanceof SyntaxError || (error instanceof Error && error.name === 'ZodError');

/**
 * Only a transient revocation failure keeps the grant, unless forced, so logout can retry.
 * A kept grant is marked so that the profile stays blocked until sign-out finishes.
 */
export async function signOutMcp(options: Options): Promise<SignOutResult> {
  const provider = new McpOAuthProvider({ profile: options.profile, store: options.store });
  const outcome = await storedGrant(provider)
    .then((grant) => (grant ? revokeGrant(options.profile, grant, options.request) : 'unavailable'))
    .catch(() => 'failed' as const);
  if (outcome === 'retry' && !options.force) {
    await options.marker.mark();
    return { remote: 'failed', local: 'retained' };
  }
  await provider.invalidateCredentials('all');
  await options.marker.clear();
  return { remote: outcome === 'retry' ? 'failed' : outcome, local: 'cleared' };
}

/**
 * Runs before a sign-in that replaces a grant an unfinished logout kept. The kept grant is
 * revoked, or handed to the pending list when the server cannot confirm that, and then deleted,
 * so the new sign-in can never refresh or reuse it.
 */
export async function retireKeptGrant(options: {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly pending: PendingRevocations;
  readonly request: typeof fetch;
}): Promise<void> {
  const provider = new McpOAuthProvider({ profile: options.profile, store: options.store });
  const grant = await storedGrant(provider).catch(() => undefined);
  if (grant && !(await revocationFinished(options.profile, grant, options.request)))
    await options.pending.add(grant);
  await provider.invalidateCredentials('all');
}

/** A command spends at most this long on the pending list, so it is never held up for long. */
const PENDING_RETRY_DEADLINE = 10_000;
const withDeadline =
  (request: typeof fetch, deadline: AbortSignal): typeof fetch =>
  (input, init) =>
    request(input, {
      ...init,
      signal: init?.signal ? AbortSignal.any([init.signal, deadline]) : deadline,
    });
/**
 * Best effort, run by commands that use a profile: retries revoking grants that an earlier
 * sign-in replaced, and forgets each one only once the server revokes it or offers no
 * revocation. It never waits for a busy profile and never fails the command.
 */
export async function retryPendingRevocations(
  directory: string,
  profile: CliProfile,
  request: typeof fetch = fetch,
): Promise<void> {
  const pending = new FilePendingRevocations(directory, profile.id);
  const retry = async () => {
    const deadline = AbortSignal.timeout(PENDING_RETRY_DEADLINE);
    const remaining: UnrevokedGrant[] = [];
    for (const grant of await pending.entries())
      if (!(await revocationFinished(profile, grant, withDeadline(request, deadline))))
        remaining.push(grant);
    await pending.replace(remaining);
  };
  const waiting = await pending.entries().catch(() => []);
  if (waiting.length > 0) await withProfileLock(directory, profile.id, retry, 0).catch(() => {});
}

async function storedGrant(provider: McpOAuthProvider): Promise<UnrevokedGrant | undefined> {
  const tokens = await provider.tokens();
  if (!tokens) return undefined;
  const client = await provider.clientInformation();
  if (!client) return undefined;
  return {
    clientId: client.client_id,
    accessToken: tokens.access_token,
    ...(tokens.refresh_token ? { refreshToken: tokens.refresh_token } : {}),
  };
}
/** Only a revoked grant, or a server without revocation, can be forgotten safely. */
const revocationFinished = (
  profile: CliProfile,
  grant: UnrevokedGrant,
  request: typeof fetch,
): Promise<boolean> =>
  revokeGrant(profile, grant, request).then(
    (outcome) => outcome === 'revoked' || outcome === 'unavailable',
    () => false,
  );
async function revokeGrant(
  profile: CliProfile,
  grant: UnrevokedGrant,
  request: typeof fetch,
): Promise<RevocationStatus | 'retry'> {
  const guarded = transientFailures(secureFetch(profile.development, request));
  return revokeRemotely({ request: guarded, grant }, profile).catch((error: unknown) => {
    if (error instanceof TransientRevocationError) return 'retry' as const;
    throw error;
  });
}
async function revokeRemotely(
  input: { request: typeof fetch; grant: UnrevokedGrant },
  profile: CliProfile,
): Promise<RevocationStatus> {
  const metadata = await discoverAuthorizationServerMetadata(profile.mcpIssuer, {
    fetchFn: input.request,
  }).catch((error: unknown) => {
    if (!unparseable(error)) throw error;
    throw new TransientRevocationError('MCP authorization server metadata was unreadable.', {
      cause: error,
    });
  });
  const endpoint = revocationSchema.parse(metadata).revocation_endpoint;
  if (!endpoint) return 'unavailable';
  const target = { request: input.request, endpoint, clientId: input.grant.clientId };
  await revokeToken({ ...target, token: input.grant.refreshToken, hint: 'refresh_token' });
  await revokeToken({ ...target, token: input.grant.accessToken, hint: 'access_token' });
  return 'revoked';
}
async function revokeToken(input: {
  request: typeof fetch;
  endpoint: string;
  clientId: string;
  token: string | undefined;
  hint: string;
}): Promise<void> {
  if (!input.token) return;
  const response = await input.request(input.endpoint, {
    method: 'POST',
    headers: { 'content-type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      token: input.token,
      token_type_hint: input.hint,
      client_id: input.clientId,
    }),
  });
  await response.body?.cancel();
  if (!response.ok) throw new Error('MCP grant revocation failed.');
}
