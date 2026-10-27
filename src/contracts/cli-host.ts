import { loopbackCliHost } from './cli-url.js';

export type CliHostRole = 'resource' | 'mcpIssuer' | 'accountIssuer' | 'accountProvider';
export type CliHostScope = {
  readonly appOrigin: string;
  readonly development: boolean;
  /** Once known, a WorkOS account issuer lets a self-hosted app use WorkOS's account API. */
  readonly accountIssuer?: string;
};
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
/**
 * A WorkOS account issuer's host: an AuthKit tenant, which is one DNS label under authkit.app
 * (deeper names aren't tenants), or WorkOS's own API. Only over HTTPS on the default port.
 */
const WORKOS_ISSUER_HOST =
  /^(?:[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?\.authkit\.app|api\.workos\.com)$/u;
const workosIssuer = (url: string | undefined): boolean => {
  const parsed = URL.parse(url ?? '');
  return parsed?.protocol === 'https:' && WORKOS_ISSUER_HOST.test(parsed.host);
};
/**
 * Exact hosts each known app uses. Never match by suffix. Its issuers must be exactly
 * https://<host>, and the app's own host serves only APP_HOST_ROLES.
 */
const DEPLOYMENT_HOSTS = new Map<string, Readonly<Record<CliHostRole, readonly string[]>>>([
  // Account issuer: the "Durin Production" WorkOS environment's verified AuthKit custom domain,
  // which https://auth.getdurin.com/.well-known/openid-configuration names as its issuer.
  [
    'app.getdurin.com',
    {
      resource: ['mcp.getdurin.com'],
      mcpIssuer: ['auth.getdurin.com'],
      accountIssuer: ['auth.getdurin.com'],
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

/** Hostname and port, which a URL leaves out when it is the default for its scheme. */
const hostOf = (value: string): string => (URL.canParse(value) ? new URL(value).host : '');
const loopback = (host: string): boolean => loopbackCliHost(new URL(`http://${host}`).hostname);
/** The app's host without one trailing dot, so app.getdurin.com. is still production. */
const appHostOf = (appOrigin: string): string => {
  if (!URL.canParse(appOrigin)) return '';
  const { hostname, port } = new URL(appOrigin);
  return `${hostname.replace(/\.$/u, '')}${port ? `:${port}` : ''}`;
};
const selfHosted = (scope: CliHostScope): boolean =>
  !DEPLOYMENT_HOSTS.has(appHostOf(scope.appOrigin));
/** A self-hosted app may sign accounts in with WorkOS; only then may it reach WorkOS's API. */
const selfHostedHosts = (role: CliHostRole, scope: CliHostScope): readonly string[] =>
  role === 'accountProvider' && workosIssuer(scope.accountIssuer) ? [WORKOS_API] : [];
/** A known app's own host is never one of its issuers. A self-hosted app's serves every role. */
const APP_HOST_ROLES = new Set<CliHostRole>(['resource', 'accountProvider']);
/** The app's own host, both as written in --app-url and without its trailing dot. */
const appHosts = (role: CliHostRole, scope: CliHostScope): readonly string[] =>
  selfHosted(scope) || APP_HOST_ROLES.has(role)
    ? [appHostOf(scope.appOrigin), hostOf(scope.appOrigin)]
    : [];
const expectedHosts = (role: CliHostRole, scope: CliHostScope): readonly string[] => {
  const app = appHostOf(scope.appOrigin);
  const listed = DEPLOYMENT_HOSTS.get(app)?.[role] ?? selfHostedHosts(role, scope);
  return [...new Set([...listed, ...appHosts(role, scope)])].filter(
    (host) => host && (scope.development || !loopback(host)),
  );
};
const allowsWorkosIssuer = (role: CliHostRole, scope: CliHostScope): boolean =>
  role === 'accountIssuer' && selfHosted(scope);
const pinnedIssuer = (role: CliHostRole, scope: CliHostScope): boolean =>
  (role === 'mcpIssuer' || role === 'accountIssuer') && !selfHosted(scope);
/** Exactly https://<host>, with at most one trailing slash: no path, query, fragment or user. */
const exactIssuer = (url: string, host: string): boolean =>
  URL.parse(url)?.href === `https://${host}/`;
const allowedRemote = (role: CliHostRole, url: string, scope: CliHostScope): boolean =>
  pinnedIssuer(role, scope)
    ? expectedHosts(role, scope).some((expected) => exactIssuer(url, expected))
    : expectedHosts(role, scope).includes(hostOf(url)) ||
      (allowsWorkosIssuer(role, scope) && workosIssuer(url));
const allowedHost = (role: CliHostRole, url: string, scope: CliHostScope): boolean =>
  loopback(hostOf(url)) ? scope.development : allowedRemote(role, url, scope);
const describeExpected = (role: CliHostRole, scope: CliHostScope): string => {
  const workosIssuers = allowsWorkosIssuer(role, scope)
    ? ['a WorkOS issuer (<tenant>.authkit.app or api.workos.com)']
    : [];
  const expected = [...expectedHosts(role, scope), ...workosIssuers];
  return expected.length
    ? `, but the durin app at ${appHostOf(scope.appOrigin)} uses ${expected.join(' or ')}`
    : '';
};
const workosProviderHint = (role: CliHostRole, host: string, scope: CliHostScope): string =>
  role === 'accountProvider' && host === WORKOS_API && selfHosted(scope)
    ? ' A self-hosted durin app uses api.workos.com only when its account sign-in issuer is a WorkOS issuer.'
    : '';
const describeProblem = (role: CliHostRole, host: string, scope: CliHostScope): string => {
  if (pinnedIssuer(role, scope) && expectedHosts(role, scope).includes(host))
    return `The ${ROLE_NAMES[role]} must be exactly ${host} over HTTPS, with no path, query or user name.`;
  const local = loopback(host) ? ' Local addresses are only allowed with --dev.' : '';
  const uses = describeExpected(role, scope);
  return `The ${ROLE_NAMES[role]} is on ${host}${uses}.${local}${workosProviderHint(role, host, scope)}`;
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
  const host = hostOf(url);
  if (!host) return `The ${ROLE_NAMES[role]} is not a valid web address.`;
  return allowedHost(role, url, scope) ? undefined : describeProblem(role, host, scope);
}

/** The hosts a request may reach. Loopback hosts are reachable only in development. */
export type CliHostAllowlist = { readonly hosts: readonly string[]; readonly development: boolean };
/**
 * Every host setup may contact for the deployment at `scope.appOrigin`. A self-hosted app's list
 * has api.workos.com only once `scope.accountIssuer` is known to be a WorkOS issuer.
 */
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
  hosts: [...new Set([hostOf(profile.resource), hostOf(profile.mcpIssuer)])],
  development: profile.development,
});
const listedHost = (host: string, allowlist: CliHostAllowlist): boolean =>
  loopback(host) ? allowlist.development : allowlist.hosts.includes(host);
const unlistedReason = (host: string, allowlist: CliHostAllowlist): string =>
  loopback(host)
    ? 'Local addresses are only allowed with --dev.'
    : `It isn’t one of the hosts this durin deployment uses (${allowlist.hosts.join(', ')}).`;
/** Explains, in plain language, why `url` is not on `allowlist`; undefined when it is. */
export function unlistedCliHost(url: string, allowlist: CliHostAllowlist): string | undefined {
  const host = hostOf(url);
  if (!host) return 'durin didn’t send the request: its address is not a valid web address.';
  if (listedHost(host, allowlist)) return undefined;
  return `durin didn’t contact ${host}. ${unlistedReason(host, allowlist)} Nothing was sent to it.`;
}
