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
export async function signOutMcp(options: Options): Promise<RevocationStatus> {
  const provider = new McpOAuthProvider({ profile: options.profile, store: options.store });
  try {
    return await revokeGrant(options, provider);
  } catch {
    return 'failed';
  } finally {
    await provider.invalidateCredentials('all');
  }
}
async function revokeGrant(
  options: Options,
  provider: McpOAuthProvider,
): Promise<RevocationStatus> {
  const tokens = await provider.tokens();
  if (!tokens) return 'revoked';
  const client = await provider.clientInformation();
  if (!client) return 'unavailable';
  const request = secureFetch(options.profile.development, options.request);
  const metadata = await discoverAuthorizationServerMetadata(options.profile.mcpIssuer, {
    fetchFn: request,
  });
  const endpoint = revocationSchema.parse(metadata).revocation_endpoint;
  if (!endpoint) return 'unavailable';
  const input = { request, endpoint, clientId: client.client_id };
  await revokeToken({ ...input, token: tokens.refresh_token, hint: 'refresh_token' });
  await revokeToken({ ...input, token: tokens.access_token, hint: 'access_token' });
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
