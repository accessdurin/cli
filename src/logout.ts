import { discoverAuthorizationServerMetadata } from '@modelcontextprotocol/client';
import { z } from 'zod';
import { CliError } from './cli-options.js';
import type { CliProfile } from './contracts/cli-profile.js';
import type { CredentialStore } from './ports/credential-store.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { secureFetch } from './safe-fetch.js';

type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
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

/** Only a transient revocation failure keeps the grant, unless forced, so logout can retry. */
export async function signOutMcp(options: Options): Promise<SignOutResult> {
  const provider = new McpOAuthProvider({ profile: options.profile, store: options.store });
  const outcome = await revokeGrant(options, provider).catch(() => 'failed' as const);
  if (outcome === 'retry' && !options.force) return { remote: 'failed', local: 'retained' };
  await provider.invalidateCredentials('all');
  return { remote: outcome === 'retry' ? 'failed' : outcome, local: 'cleared' };
}
async function revokeGrant(
  options: Options,
  provider: McpOAuthProvider,
): Promise<RevocationStatus | 'retry'> {
  const tokens = await provider.tokens();
  if (!tokens) return 'unavailable';
  const client = await provider.clientInformation();
  if (!client) return 'unavailable';
  const request = transientFailures(secureFetch(options.profile.development, options.request));
  return revokeRemotely({ request, clientId: client.client_id, tokens }, options.profile).catch(
    (error: unknown) => {
      if (error instanceof TransientRevocationError) return 'retry' as const;
      throw error;
    },
  );
}
async function revokeRemotely(
  input: {
    request: typeof fetch;
    clientId: string;
    tokens: { refresh_token?: string | undefined; access_token: string };
  },
  profile: CliProfile,
): Promise<RevocationStatus> {
  const metadata = await discoverAuthorizationServerMetadata(profile.mcpIssuer, {
    fetchFn: input.request,
  });
  const endpoint = revocationSchema.parse(metadata).revocation_endpoint;
  if (!endpoint) return 'unavailable';
  const target = { request: input.request, endpoint, clientId: input.clientId };
  await revokeToken({ ...target, token: input.tokens.refresh_token, hint: 'refresh_token' });
  await revokeToken({ ...target, token: input.tokens.access_token, hint: 'access_token' });
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
