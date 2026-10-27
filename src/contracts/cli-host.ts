import { loopbackCliHost } from './cli-url.js';

export type CliHostRole = 'resource' | 'mcpIssuer';
export type CliHostScope = { readonly appOrigin: string; readonly development: boolean };
export const cliHostRoles: readonly CliHostRole[] = ['resource', 'mcpIssuer'];

/** Exact hosts each known app uses besides its own. Never match by suffix. */
const DEPLOYMENT_HOSTS = new Map<string, Readonly<Record<CliHostRole, readonly string[]>>>([
  ['app.getdurin.com', { resource: ['mcp.getdurin.com'], mcpIssuer: ['auth.getdurin.com'] }],
  // Fill mcpIssuer once the staging MCP issuer exists; until then staging setup fails closed.
  ['moria.app.getdurin.com', { resource: ['moria.mcp.getdurin.com'], mcpIssuer: [] }],
]);
const ROLE_NAMES: Readonly<Record<CliHostRole, string>> = {
  resource: 'MCP endpoint',
  mcpIssuer: 'MCP sign-in server',
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
