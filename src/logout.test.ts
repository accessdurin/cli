import { expect, it } from 'vitest';
import { signOutMcp } from './logout.js';
import { createProfile } from './profiles.js';
import { McpOAuthProvider } from './mcp-oauth.js';

it('revokes this profile grant where supported and removes only its local credentials', async () => {
  const profile = createProfile({
    appOrigin: 'https://app.example.test',
    accountIssuer: 'https://account.example.test',
    accountClientId: 'account',
    principalId: 'user_one',
    workosOrganizationId: 'org_one',
    organizationId: 'org_internal',
    environment: 'production',
    resource: `https://mcp.example.test/mcp/u/${'a'.repeat(64)}`,
    mcpIssuer: 'https://issuer.example.test',
    development: false,
  });
  const values = new Map<string, string>([['unrelated', 'keep']]);
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
  const provider = new McpOAuthProvider({ profile, store });
  await provider.saveClientInformation({ client_id: 'client_one', issuer: profile.mcpIssuer });
  await provider.saveTokens({
    access_token: 'synthetic-access',
    refresh_token: 'synthetic-refresh',
    token_type: 'Bearer',
    issuer: profile.mcpIssuer,
  });
  const revoked: string[] = [];
  const request: typeof fetch = async (url, init) => {
    if (String(url).includes('.well-known'))
      return Response.json({
        issuer: profile.mcpIssuer,
        authorization_endpoint: 'https://issuer.example.test/authorize',
        token_endpoint: 'https://issuer.example.test/token',
        response_types_supported: ['code'],
        revocation_endpoint: 'https://issuer.example.test/revoke',
      });
    revoked.push(new URLSearchParams(String(init?.body)).get('token')!);
    return new Response(null, { status: 200 });
  };
  expect(await signOutMcp({ profile, store, request })).toBe('revoked');
  expect(revoked).toEqual(['synthetic-refresh', 'synthetic-access']);
  expect([...values.entries()]).toEqual([['unrelated', 'keep']]);
});
