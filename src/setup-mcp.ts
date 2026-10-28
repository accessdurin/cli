import { auth, discoverOAuthProtectedResourceMetadata } from '@modelcontextprotocol/client';
import { z } from 'zod';
import type { CredentialStore } from './ports/credential-store.js';
import type { RevocationMarker } from './ports/revocation-marker.js';
import type { PendingRevocations } from './ports/pending-revocations.js';
import type { CliProfile } from './contracts/cli-profile.js';
import { safeCliUrl } from './contracts/cli-url.js';
import {
  canonicalIssuer,
  profileHostAllowlist,
  untrustedCliHost,
  type CliHostRole,
  type CliHostScope,
} from './contracts/cli-host.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { listenForAuthorization } from './oauth-callback.js';
import { CliError } from './cli-options.js';
import { RemoteMcpClient } from './remote-mcp.js';
import { retireKeptGrant } from './logout.js';
import { secureFetch } from './safe-fetch.js';

export async function mcpIssuer(
  resource: string,
  request: typeof fetch,
  scope: CliHostScope,
): Promise<string> {
  if (!safeCliUrl(resource, scope.development))
    throw new CliError('The personal MCP endpoint is unsafe.');
  assertExpectedHost('resource', resource, scope);
  const noted: NotedIssuers = { others: [] };
  const metadata = await discoverOAuthProtectedResourceMetadata(
    resource,
    undefined,
    observingProtectedResource(resource, request, noted),
  );
  return pinnedMcpIssuer(resource, metadata, noted, scope);
}
const pinnedMcpIssuer = (
  resource: string,
  metadata: {
    readonly resource?: string | undefined;
    readonly authorization_servers?: string[] | undefined;
  },
  captured: NotedIssuers,
  scope: CliHostScope,
): string => {
  const issuers = authorizationServers(metadata.authorization_servers);
  if (![metadata.resource === resource, issuers.length === 1].every(Boolean))
    throw new CliError(
      'MCP discovery must identify the exact personal resource and one trusted issuer.',
    );
  const issuer = agreedIssuer(captured, issuers[0] ?? '', scope);
  if (!validIssuer(issuer, scope.development))
    throw new CliError('Unsafe MCP authorization issuer.');
  assertExpectedHost('mcpIssuer', issuer, scope);
  return canonicalIssuer('mcpIssuer', issuer, scope);
};
function assertExpectedHost(role: CliHostRole, url: string, scope: CliHostScope): void {
  const problem = untrustedCliHost(role, url, scope);
  if (problem) throw new CliError(`${problem} Check --app-url, or ask your durin administrator.`);
}

type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly marker: RevocationMarker;
  readonly pending: PendingRevocations;
  readonly request: typeof fetch;
  readonly open: (url: string) => Promise<void>;
};
/**
 * Runs under the profile lock. A grant kept by an unfinished logout is retired, never reused.
 * Every request, including those to endpoints the server's metadata names, stays on the
 * profile's own hosts.
 */
export async function authorizeMcp(input: Options): Promise<number> {
  const options = {
    ...input,
    request: secureFetch(profileHostAllowlist(input.profile), input.request),
  };
  if (await options.marker.pending()) await retireKeptGrant(options);
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
      fetchFn: options.request,
    });
    if (result === 'REDIRECT') await exchangeCode(options, provider, callback.result);
    await options.marker.clear();
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
type NotedIssuers = { pinned?: string; others: string[] };
const ISSUERS_DISAGREE =
  'MCP discovery found protected-resource documents that name different sign-in servers. Nothing was saved.';
const refuseDifferentIssuer = (pinned: string | undefined, scope: CliHostScope): never => {
  if (pinned) assertExpectedHost('mcpIssuer', pinned, scope);
  throw new CliError(ISSUERS_DISAGREE);
};
/** One trailing slash does not make a different issuer. Nothing else is folded in. */
const sameIssuer = (left: string, right: string, scope: CliHostScope): boolean =>
  canonicalIssuer('mcpIssuer', left, scope) === canonicalIssuer('mcpIssuer', right, scope);
const agreedIssuer = (noted: NotedIssuers, sdkIssuer: string, scope: CliHostScope): string => {
  if (!noted.pinned || !sameIssuer(noted.pinned, sdkIssuer, scope))
    refuseDifferentIssuer(noted.pinned, scope);
  if (noted.others.some((issuer) => !sameIssuer(issuer, sdkIssuer, scope)))
    throw new CliError(ISSUERS_DISAGREE);
  return sdkIssuer;
};
const protectedResourceSchema = z.object({
  authorization_servers: z.array(z.string()).min(1),
});
const documentIssuer = async (response: Response): Promise<string | undefined> => {
  const body: unknown = await response
    .clone()
    .json()
    .catch(() => undefined);
  const parsed = protectedResourceSchema.safeParse(body);
  return parsed.success ? parsed.data.authorization_servers[0] : undefined;
};
const sameOrigin = (target: string, origin: string): boolean =>
  URL.canParse(target) && new URL(target).origin === origin;
const pinsIssuer = (response: Response, target: string, origin: string): boolean =>
  response.ok && !response.redirected && sameOrigin(target, origin);
const isPinnedDocument = (target: string, response: Response, origin: string): boolean =>
  target.includes('oauth-protected-resource') && pinsIssuer(response, target, origin);
/** Only a 2xx document on the resource origin is the issuer durin will pin. */
const noteProtectedResource = async (
  target: string,
  response: Response,
  origin: string,
  noted: NotedIssuers,
): Promise<void> => {
  if (!isPinnedDocument(target, response, origin)) return;
  const issuer = await documentIssuer(response);
  if (!issuer) return;
  if (noted.pinned) noted.others.push(noted.pinned);
  noted.pinned = issuer;
};
const observingProtectedResource = (
  resource: string,
  request: typeof fetch,
  noted: NotedIssuers,
): typeof fetch => {
  const origin = new URL(resource).origin;
  const observe: typeof fetch = async (input, init) => {
    const response = await request(input, init);
    const target = input instanceof Request ? input.url : String(input);
    await noteProtectedResource(target, response, origin, noted);
    return response;
  };
  return observe;
};
const validIssuer = (issuer: string | undefined, development: boolean): boolean =>
  Boolean(issuer && safeCliUrl(issuer, development));
