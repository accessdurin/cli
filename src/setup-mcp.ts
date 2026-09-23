import { auth, discoverOAuthProtectedResourceMetadata } from '@modelcontextprotocol/client';
import type { CredentialStore } from './ports/credential-store.js';
import type { CliProfile } from './contracts/cli-profile.js';
import { safeCliUrl } from './contracts/cli-url.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { listenForAuthorization } from './oauth-callback.js';
import { CliError } from './cli-options.js';
import { RemoteMcpClient } from './remote-mcp.js';

export async function mcpIssuer(
  resource: string,
  request: typeof fetch,
  development: boolean,
): Promise<string> {
  if (!safeCliUrl(resource, development))
    throw new CliError('The personal MCP endpoint is unsafe.');
  const metadata = await discoverOAuthProtectedResourceMetadata(resource, undefined, request);
  const issuers = authorizationServers(metadata.authorization_servers);
  if (![metadata.resource === resource, issuers.length === 1].every(Boolean))
    throw new CliError(
      'MCP discovery must identify the exact personal resource and one trusted issuer.',
    );
  const issuer = issuers[0];
  if (!validIssuer(issuer, development)) throw new CliError('Unsafe MCP authorization issuer.');
  return String(issuer);
}

type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly request: typeof fetch;
  readonly open: (url: string) => Promise<void>;
};
export async function authorizeMcp(options: Options): Promise<number> {
  const callback = await listenForAuthorization({ issuer: options.profile.mcpIssuer });
  const provider = new McpOAuthProvider({
    profile: options.profile,
    store: options.store,
    authorization: {
      redirectUrl: callback.redirectUrl,
      state: callback.state,
      open: (url) => options.open(url.href),
    },
  });
  try {
    const result = await auth(provider, {
      serverUrl: options.profile.resource,
      scope: 'durin:read durin:write durin:execute',
      fetchFn: options.request,
    });
    if (result === 'REDIRECT') await exchangeCode(options, provider, callback.result);
    return await authenticatedCatalog(options, provider);
  } finally {
    await callback.close();
  }
}
async function exchangeCode(
  options: Options,
  provider: McpOAuthProvider,
  pending: Promise<{ code: string; issuer?: string }>,
): Promise<void> {
  const returned = await pending;
  const result = await auth(provider, {
    serverUrl: options.profile.resource,
    authorizationCode: returned.code,
    ...(returned.issuer ? { iss: returned.issuer } : {}),
    fetchFn: options.request,
  });
  if (result !== 'AUTHORIZED')
    throw new CliError('MCP authorization is incomplete. Run durin to resume.');
}
async function authenticatedCatalog(options: Options, provider: McpOAuthProvider): Promise<number> {
  const client = new RemoteMcpClient({
    resource: options.profile.resource,
    development: options.profile.development,
    authorization: provider,
    fetch: options.request,
  });
  try {
    await client.connect();
    return (await client.listTools()).tools.length;
  } finally {
    await client.close();
  }
}

const authorizationServers = (issuers?: string[]): string[] => issuers ?? [];
const validIssuer = (issuer: string | undefined, development: boolean): boolean =>
  Boolean(issuer && safeCliUrl(issuer, development));
