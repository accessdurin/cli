import { createHash } from 'node:crypto';
import { expect, it } from 'vitest';
import { auth } from '@modelcontextprotocol/client';
import { McpOAuthProvider } from './mcp-oauth.js';
import { createProfile } from './profiles.js';

const resource = `https://mcp.example.test/mcp/u/${'a'.repeat(64)}`;
const issuer = 'https://auth.example.test/';
const bodyText = (init?: RequestInit): string => String(init?.body);
const profile = createProfile({
  appOrigin: 'https://app.example.test',
  accountIssuer: issuer,
  accountClientId: 'account_client',
  principalId: 'user_one',
  workosOrganizationId: 'org_provider',
  organizationId: 'org_one',
  environment: 'production',
  resource,
  mcpIssuer: issuer,
  development: false,
});

it('authorizes MCP separately with PKCE and the exact organization resource, then securely resumes', async () => {
  const values = new Map<string, string>();
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async (key: string) => {
      values.delete(key);
    },
  };
  const redirects: URL[] = [];
  const provider = new McpOAuthProvider({
    profile,
    store,
    authorization: {
      redirectUrl: 'http://127.0.0.1:12345/oauth/callback',
      state: 'synthetic-unique-state',
      open: async (url) => {
        redirects.push(url);
      },
    },
  });
  const exchanges: URLSearchParams[] = [];
  const fetcher: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    if (url.pathname.includes('oauth-protected-resource'))
      return Response.json({ resource, authorization_servers: [issuer] });
    if (url.pathname.includes('.well-known'))
      return Response.json({
        issuer,
        authorization_endpoint: `${issuer}authorize`,
        token_endpoint: `${issuer}token`,
        registration_endpoint: `${issuer}register`,
        response_types_supported: ['code'],
        code_challenge_methods_supported: ['S256'],
        token_endpoint_auth_methods_supported: ['none'],
        authorization_response_iss_parameter_supported: true,
      });
    if (url.pathname === '/register')
      return Response.json({ ...JSON.parse(bodyText(init)), client_id: 'mcp_public_client' });
    exchanges.push(new URLSearchParams(bodyText(init)));
    return Response.json({
      access_token: 'synthetic-mcp-access',
      refresh_token: 'synthetic-mcp-refresh',
      token_type: 'Bearer',
      expires_in: 3600,
    });
  };
  expect(await auth(provider, { serverUrl: resource, fetchFn: fetcher })).toBe('REDIRECT');
  const authorization = redirects[0]!;
  expect(authorization.searchParams.get('resource')).toBe(resource);
  expect(authorization.searchParams.get('state')).toBe('synthetic-unique-state');
  expect(authorization.searchParams.get('code_challenge_method')).toBe('S256');
  expect(
    await auth(provider, {
      serverUrl: resource,
      authorizationCode: 'synthetic-code',
      iss: issuer,
      fetchFn: fetcher,
    }),
  ).toBe('AUTHORIZED');
  const exchange = exchanges[0]!;
  expect(exchange.get('resource')).toBe(resource);
  expect(exchange.get('client_id')).toBe('mcp_public_client');
  expect(exchange.has('client_secret')).toBe(false);
  expect(createHash('sha256').update(exchange.get('code_verifier')!).digest('base64url')).toBe(
    authorization.searchParams.get('code_challenge'),
  );
  const resumed = new McpOAuthProvider({ profile, store });
  expect(await resumed.tokens()).toMatchObject({ access_token: 'synthetic-mcp-access', issuer });
});

it('does not reuse a native client registration bound to a previous callback port', async () => {
  const values = new Map<string, string>();
  const store = {
    read: async (key: string) => values.get(key) ?? null,
    write: async (key: string, value: string) => {
      values.set(key, value);
    },
    remove: async () => {},
  };
  const previous = new McpOAuthProvider({ profile, store });
  await previous.saveClientInformation({
    client_id: 'mcp_public_client',
    issuer,
    redirect_uris: ['http://127.0.0.1:12345/oauth/callback'],
  });
  const restarted = new McpOAuthProvider({
    profile,
    store,
    authorization: {
      redirectUrl: 'http://127.0.0.1:54321/oauth/callback',
      state: 'new-state',
      open: async () => {},
    },
  });
  expect(await restarted.clientInformation({ issuer })).toBeUndefined();
});
