// MAR-828 acceptance: Tess's QA host matrix for cli#8 (qa-host-pin.test.ts), as an assertion table.
// Expected outcomes are her 5ede0ff results, except "port 8443" and the loopback self-host rows
// (127.0.0.2, localhost., 0.0.0.0, [::ffff:127.0.0.1] without --dev), which must now be rejected.
import { expect, it } from 'vitest';
import { untrustedCliHost, type CliHostScope } from './cli-host.js';
import { safeCliUrl } from './cli-url.js';

const PROD = { appOrigin: 'https://app.getdurin.com', development: false };
const STG = { appOrigin: 'https://moria.app.getdurin.com', development: false };
type Row = readonly [
  name: string,
  role: 'resource' | 'mcpIssuer',
  url: string,
  scope: CliHostScope,
  expected: 'accept' | 'reject',
];
// prettier-ignore
const rows: readonly Row[] = [
  ['prod resource exact', 'resource', 'https://mcp.getdurin.com/mcp/u/x', PROD, 'accept'],
  ['prod issuer exact', 'mcpIssuer', 'https://auth.getdurin.com/', PROD, 'accept'],
  ['suffix lookalike', 'resource', 'https://getdurin.com.evil.com/mcp', PROD, 'reject'],
  ['mcp suffix lookalike', 'resource', 'https://mcp.getdurin.com.evil.com/mcp', PROD, 'reject'],
  ['prefix lookalike', 'resource', 'https://evil-getdurin.com/mcp', PROD, 'reject'],
  ['userinfo trick', 'resource', 'https://mcp.getdurin.com@evil.com/mcp', PROD, 'reject'],
  ['userinfo on real host', 'resource', 'https://evil.com@mcp.getdurin.com/mcp', PROD, 'reject'],
  ['uppercase host', 'resource', 'https://MCP.GETDURIN.COM/mcp', PROD, 'accept'],
  ['trailing dot', 'resource', 'https://mcp.getdurin.com./mcp', PROD, 'reject'],
  ['punycode/IDN (dotless i)', 'resource', 'https://mcp.getdurın.com/mcp', PROD, 'reject'],
  ['cyrillic a', 'mcpIssuer', 'https://аuth.getdurin.com/', PROD, 'reject'],
  ['port 8443', 'resource', 'https://mcp.getdurin.com:8443/mcp', PROD, 'reject'],
  ['port 443 explicit', 'resource', 'https://mcp.getdurin.com:443/mcp', PROD, 'accept'],
  ['http scheme', 'resource', 'http://mcp.getdurin.com/mcp', PROD, 'reject'],
  ['backslash trick', 'resource', 'https://evil.com\\@mcp.getdurin.com/mcp', PROD, 'reject'],
  ['path trick', 'resource', 'https://evil.com/mcp.getdurin.com/mcp', PROD, 'reject'],
  ['encoded @ in host', 'resource', 'https://mcp.getdurin.com%40evil.com/mcp', PROD, 'reject'],
  ['localhost no dev', 'resource', 'https://localhost/mcp', PROD, 'reject'],
  ['[::1] no dev', 'resource', 'https://[::1]/mcp', PROD, 'reject'],
  ['127.0.0.2 no dev', 'resource', 'https://127.0.0.2/mcp', PROD, 'reject'],
  ['localhost.evil.com', 'resource', 'https://localhost.evil.com/mcp', PROD, 'reject'],
  ['[::1] dev, prod app', 'resource', 'https://[::1]/mcp', { ...PROD, development: true }, 'accept'],
  ['http localhost dev', 'resource', 'http://localhost:8787/mcp', { ...PROD, development: true }, 'accept'],
  ['staging resource on staging', 'resource', 'https://moria.mcp.getdurin.com/mcp', STG, 'accept'],
  ['staging issuer on staging', 'mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app/', STG, 'accept'],
  ['staging issuer UPPERCASE', 'mcpIssuer', 'https://RESILIENT-TIGER-64-STAGING.authkit.app/', STG, 'accept'],
  ['other tenant authkit', 'mcpIssuer', 'https://other-tenant.authkit.app/', STG, 'reject'],
  ['bare authkit.app', 'mcpIssuer', 'https://authkit.app/', STG, 'reject'],
  ['staging issuer .evil.com', 'mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app.evil.com/', STG, 'reject'],
  ['staging issuer on PROD', 'mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app/', PROD, 'reject'],
  ['staging resource on PROD', 'resource', 'https://moria.mcp.getdurin.com/mcp', PROD, 'reject'],
  ['prod issuer on staging', 'mcpIssuer', 'https://auth.getdurin.com/', STG, 'reject'],
  ['moria.app.getdurin.com (callback host) as issuer on staging', 'mcpIssuer', 'https://moria.app.getdurin.com/', STG, 'reject'],
  ['moria callback host as issuer on PROD', 'mcpIssuer', 'https://moria.app.getdurin.com/', PROD, 'reject'],
  ['app host as resource prod', 'resource', 'https://app.getdurin.com/mcp', PROD, 'accept'],
  ['staging issuer as resource', 'resource', 'https://resilient-tiger-64-staging.authkit.app/mcp', STG, 'reject'],
  ['app-url trailing dot (staging)', 'mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app/', { appOrigin: 'https://moria.app.getdurin.com.', development: false }, 'accept'],
  ['app-url uppercase staging', 'mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app/', { appOrigin: 'https://MORIA.APP.GETDURIN.COM', development: false }, 'accept'],
  ['app-url 127.0.0.2 no dev (self host)', 'resource', 'https://127.0.0.2/mcp', { appOrigin: 'https://127.0.0.2', development: false }, 'reject'],
  ['app-url localhost. no dev (self host)', 'resource', 'https://localhost./mcp', { appOrigin: 'https://localhost.', development: false }, 'reject'],
  ['app-url 0.0.0.0 no dev (self host)', 'resource', 'https://0.0.0.0/mcp', { appOrigin: 'https://0.0.0.0', development: false }, 'reject'],
  ['app-url [::ffff:127.0.0.1] no dev', 'resource', 'https://[::ffff:127.0.0.1]/mcp', { appOrigin: 'https://[::ffff:127.0.0.1]', development: false }, 'reject'],
  ['app-url 127.1 no dev', 'resource', 'https://127.1/mcp', { appOrigin: 'https://127.1', development: false }, 'reject'],
  ['app-url localhost no dev (self host)', 'resource', 'https://localhost/mcp', { appOrigin: 'https://localhost', development: false }, 'reject'],
];
const outcome = (role: Row[1], url: string, scope: CliHostScope) =>
  untrustedCliHost(role, url, scope) === undefined && safeCliUrl(url, scope.development)
    ? 'accept'
    : 'reject';

it.each(rows.map(([name, role, url, scope, expected]) => ({ name, role, url, scope, expected })))(
  'QA host matrix: $name ($role $url)',
  ({ role, url, scope, expected }) => {
    expect(outcome(role, url, scope)).toBe(expected);
  },
);
