import { loopbackCliHost } from './cli-url.js';

export type CliHostRole = 'resource' | 'mcpIssuer' | 'accountIssuer' | 'accountProvider';
export type CliHostScope = { readonly appOrigin: string; readonly development: boolean };
export const cliHostRoles: readonly CliHostRole[] = [
  'resource',
  'mcpIssuer',
  'accountIssuer',
  'accountProvider',
];
/** The roles a saved profile records a URL for. */
export const cliProfileHostRoles = ['resource', 'mcpIssuer', 'accountIssuer'] as const;
/** The roles setup sends requests to. The account issuer only names who signs account tokens. */
const contactedRoles: readonly CliHostRole[] = ['resource', 'mcpIssuer', 'accountProvider'];

/**
 * The account API for device sign-in. durin's server returns exactly this as providerOrigin
 * outside loopback development (durin workers/api/src/cli-auth.ts).
 */
const WORKOS_API = 'api.workos.com';
/** Exact hosts each known app uses besides its own. Never match by suffix. */
const DEPLOYMENT_HOSTS = new Map<string, Readonly<Record<CliHostRole, readonly string[]>>>([
  // Production's account issuer (WORKOS_AUTHKIT_ISSUER) is set only at release time and isn't
  // recorded in this repo or durin's, so only the app's own host is accepted until it is added.
  [
    'app.getdurin.com',
    {
      resource: ['mcp.getdurin.com'],
      mcpIssuer: ['auth.getdurin.com'],
      accountIssuer: [],
      accountProvider: [WORKOS_API],
    },
  ],
  // Gateway: durin infra/durin.config.json (staging mcpGateway).
  // Issuers: the staging AuthKit tenant, which signs both staging MCP grants and account tokens.
  [
    'moria.app.getdurin.com',
    {
      resource: ['moria.mcp.getdurin.com'],
      mcpIssuer: ['resilient-tiger-64-staging.authkit.app'],
      accountIssuer: ['resilient-tiger-64-staging.authkit.app'],
      accountProvider: [WORKOS_API],
    },
  ],
]);
const ROLE_NAMES: Readonly<Record<CliHostRole, string>> = {
  resource: 'MCP endpoint',
  mcpIssuer: 'MCP sign-in server',
  accountIssuer: 'account sign-in issuer',
  accountProvider: 'account sign-in service',
};

const hostname = (value: string): string => (URL.canParse(value) ? new URL(value).hostname : '');
const expectedHosts = (role: CliHostRole, scope: CliHostScope): readonly string[] => {
  const app = hostname(scope.appOrigin);
  const listed = DEPLOYMENT_HOSTS.get(app)?.[role] ?? [];
  return [...listed, app].filter((host) => host && (scope.development || !loopbackCliHost(host)));
};
const allowedHost = (role: CliHostRole, host: string, scope: CliHostScope): boolean =>
  loopbackCliHost(host) ? scope.development : expectedHosts(role, scope).includes(host);
const describeProblem = (role: CliHostRole, host: string, scope: CliHostScope): string => {
  const expected = expectedHosts(role, scope);
  const uses = expected.length
    ? `, but the durin app at ${hostname(scope.appOrigin)} uses ${expected.join(' or ')}`
    : '';
  const local = loopbackCliHost(host) ? ' Local addresses are only allowed with --dev.' : '';
  return `The ${ROLE_NAMES[role]} is on ${host}${uses}.${local}`;
};

/**
 * Explains, in plain language, why `url` is not on a host that the deployment at
 * `scope.appOrigin` uses for `role`; undefined when the host is expected.
 */
export function untrustedCliHost(
  role: CliHostRole,
  url: string,
  scope: CliHostScope,
): string | undefined {
  const host = hostname(url);
  if (!host) return `The ${ROLE_NAMES[role]} is not a valid web address.`;
  return allowedHost(role, host, scope) ? undefined : describeProblem(role, host, scope);
}

/** The hosts a request may reach. Loopback hosts are reachable only in development. */
export type CliHostAllowlist = { readonly hosts: readonly string[]; readonly development: boolean };
/** Every host setup may contact for the deployment at `scope.appOrigin`. */
export const deploymentHostAllowlist = (scope: CliHostScope): CliHostAllowlist => ({
  hosts: [...new Set(contactedRoles.flatMap((role) => expectedHosts(role, scope)))],
  development: scope.development,
});
/** A saved profile's MCP requests reach only its endpoint and sign-in server. */
export const profileHostAllowlist = (profile: {
  readonly resource: string;
  readonly mcpIssuer: string;
  readonly development: boolean;
}): CliHostAllowlist => ({
  hosts: [...new Set([hostname(profile.resource), hostname(profile.mcpIssuer)])],
  development: profile.development,
});
const listedHost = (host: string, allowlist: CliHostAllowlist): boolean =>
  loopbackCliHost(host) ? allowlist.development : allowlist.hosts.includes(host);
const unlistedReason = (host: string, allowlist: CliHostAllowlist): string =>
  loopbackCliHost(host)
    ? 'Local addresses are only allowed with --dev.'
    : `It isn’t one of the hosts this durin deployment uses (${allowlist.hosts.join(', ')}).`;
/** Explains, in plain language, why `url` is not on `allowlist`; undefined when it is. */
export function unlistedCliHost(url: string, allowlist: CliHostAllowlist): string | undefined {
  const host = hostname(url);
  if (!host) return 'durin didn’t send the request: its address is not a valid web address.';
  if (listedHost(host, allowlist)) return undefined;
  return `durin didn’t contact ${host}. ${unlistedReason(host, allowlist)} Nothing was sent to it.`;
}
