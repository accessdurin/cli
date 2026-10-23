import { discoverAuthorizationServerMetadata } from '@modelcontextprotocol/client';
import { z } from 'zod';
import type { CliProfile } from './contracts/cli-profile.js';
import type { CredentialStore } from './ports/credential-store.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { secureFetch } from './safe-fetch.js';

type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly request: typeof fetch;
};
const revocationSchema = z.object({ revocation_endpoint: z.url().optional() });
export type RevocationStatus = 'revoked' | 'unavailable' | 'failed';
export type SignOutResult = {
  readonly remote: RevocationStatus;
  readonly local: 'cleared' | 'retained';
};
/** A grant whose revocation request failed stays stored so that logout can be retried. */
export async function signOutMcp(options: Options): Promise<SignOutResult> {
  const provider = new McpOAuthProvider({ profile: options.profile, store: options.store });
  const remote = await revokeGrant(options, provider).catch(() => 'failed' as const);
  if (remote === 'retry') return { remote: 'failed', local: 'retained' };
  await provider.invalidateCredentials('all');
  return { remote, local: 'cleared' };
}
async function revokeGrant(
  options: Options,
  provider: McpOAuthProvider,
): Promise<RevocationStatus | 'retry'> {
  const tokens = await provider.tokens();
  if (!tokens) return 'revoked';
  const client = await provider.clientInformation();
  if (!client) return 'unavailable';
  const request = secureFetch(options.profile.development, options.request);
  return revokeRemotely({ request, clientId: client.client_id, tokens }, options.profile).catch(
    () => 'retry' as const,
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
