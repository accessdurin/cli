import { expect, it, vi } from 'vitest';
import { authorizeMcp } from './setup-mcp.js';
import { createProfile } from './profiles.js';

vi.mock('./remote-mcp.js', () => ({
  RemoteMcpClient: class {
    connect = async () => {};
    close = async () => {};
    listTools = async () => ({ tools: [{ name: 'sandbox__read_document' }] });
  },
}));

const resource = `https://mcp.example.test/mcp/u/${'a'.repeat(64)}`;
const issuer = 'https://auth.example.test/';
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

it('authorizes with the exact personal MCP resource without injecting Durin scopes', async () => {
  const redirects: URL[] = [];
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
  const tools = await authorizeMcp({
    profile,
    store,
    request: oauthFixtureRequest,
    open: async (value) => {
      const authorization = new URL(value);
      redirects.push(authorization);
      await fetch(callbackUrl(authorization));
    },
  });
  expect(tools).toBe(1);
  expect(redirects[0]?.searchParams.get('resource')).toBe(resource);
  expect(redirects[0]?.searchParams.get('scope')).toBeNull();
});

const oauthFixtureRequest: typeof fetch = async (input, init) => {
  const url = new URL(String(input));
  const route = oauthRoutes(init).find(({ match }) => match(url));
  return route?.response() ?? Response.json(tokens());
};

const oauthRoutes = (init?: RequestInit) => [
  {
    match: (url: URL) => url.pathname.includes('oauth-protected-resource'),
    response: () => Response.json({ resource, authorization_servers: [issuer] }),
  },
  {
    match: (url: URL) => url.pathname.includes('.well-known'),
    response: () => Response.json(metadata()),
  },
  {
    match: (url: URL) => url.pathname === '/register',
    response: () =>
      Response.json({ ...JSON.parse(String(init?.body)), client_id: 'mcp_public_client' }),
  },
];

const metadata = () => ({
  issuer,
  scopes_supported: ['email'],
  authorization_endpoint: `${issuer}authorize`,
  token_endpoint: `${issuer}token`,
  registration_endpoint: `${issuer}register`,
  response_types_supported: ['code'],
  code_challenge_methods_supported: ['S256'],
  token_endpoint_auth_methods_supported: ['none'],
  authorization_response_iss_parameter_supported: true,
});

const tokens = () => ({
  access_token: 'synthetic-mcp-access',
  refresh_token: 'synthetic-mcp-refresh',
  token_type: 'Bearer',
  expires_in: 3600,
});

const callbackUrl = (authorization: URL): string => {
  const callback = new URL(authorization.searchParams.get('redirect_uri')!);
  callback.search = new URLSearchParams({
    code: 'synthetic-code',
    state: authorization.searchParams.get('state')!,
    iss: issuer,
  }).toString();
  return callback.href;
};
