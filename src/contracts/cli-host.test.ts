import { describe, expect, it } from 'vitest';
import {
  cliHostRoles,
  deploymentHostAllowlist,
  profileHostAllowlist,
  untrustedCliHost,
  type CliHostRole,
} from './cli-host.js';

const personal = `/mcp/u/${'a'.repeat(64)}`;
const production = { appOrigin: 'https://app.getdurin.com', development: false };
const staging = { appOrigin: 'https://moria.app.getdurin.com', development: false };
const custom = { appOrigin: 'https://durin.acme.test', development: false };
const local = { appOrigin: 'http://127.0.0.1:5270', development: true };
const url = (role: CliHostRole, origin: string) =>
  role === 'resource' ? `${origin}${personal}` : `${origin}/`;
const roles = (origins: readonly string[]) =>
  cliHostRoles.flatMap((role) => origins.map((origin) => [role, origin] as const));

describe('host pinning', () => {
  it.each([
    ['resource', `https://mcp.getdurin.com${personal}`],
    ['resource', `https://MCP.GetDurin.com${personal}`],
    ['mcpIssuer', 'https://auth.getdurin.com'],
    ['mcpIssuer', 'https://auth.getdurin.com/'],
  ] as const)('accepts the production %s host: %s', (role, value) => {
    expect(untrustedCliHost(role, value, production)).toBeUndefined();
  });

  it('accepts the staging MCP gateway for the staging app', () => {
    expect(
      untrustedCliHost('resource', `https://moria.mcp.getdurin.com${personal}`, staging),
    ).toBeUndefined();
  });

  it.each([
    'https://resilient-tiger-64-staging.authkit.app',
    'https://resilient-tiger-64-staging.authkit.app/',
    'https://Resilient-Tiger-64-Staging.AuthKit.app/',
  ])('accepts the staging MCP sign-in server for the staging app: %s', (value) => {
    expect(untrustedCliHost('mcpIssuer', value, staging)).toBeUndefined();
  });

  it.each([
    'https://evil-tiger-99-staging.authkit.app',
    'https://authkit.app',
    'https://attacker.resilient-tiger-64-staging.authkit.app',
    'https://resilient-tiger-64-staging.authkit.app.evil.test',
    'https://resilient-tiger-64.authkit.app',
  ])(
    'rejects a staging MCP sign-in server that is not exactly the staging issuer: %s',
    (origin) => {
      const problem = untrustedCliHost('mcpIssuer', `${origin}/`, staging);
      expect(problem).toContain(new URL(origin).hostname);
      expect(problem).toContain('resilient-tiger-64-staging.authkit.app');
    },
  );

  it('does not accept the staging sign-in server as the staging MCP endpoint', () => {
    expect(
      untrustedCliHost(
        'resource',
        `https://resilient-tiger-64-staging.authkit.app${personal}`,
        staging,
      ),
    ).toBeDefined();
  });

  it.each(cliHostRoles)(
    'accepts a %s on the app origin host for a deployment without an allowlist',
    (role) => {
      expect(untrustedCliHost(role, url(role, 'https://durin.acme.test'), custom)).toBeUndefined();
      expect(
        untrustedCliHost(role, url(role, 'https://durin.acme.test:443'), custom),
      ).toBeUndefined();
    },
  );

  it.each(cliHostRoles)(
    'requires the %s on the app’s own host to use the app’s own port as well',
    (role) => {
      const onPort = { appOrigin: 'https://durin.acme.test:8443', development: false };
      expect(
        untrustedCliHost(role, url(role, 'https://durin.acme.test:8443'), onPort),
      ).toBeUndefined();
      expect(untrustedCliHost(role, url(role, 'https://durin.acme.test'), onPort)).toContain(
        'durin.acme.test',
      );
      expect(untrustedCliHost(role, url(role, 'https://durin.acme.test:8443'), custom)).toContain(
        'durin.acme.test:8443',
      );
    },
  );

  it.each(
    (
      [
        ['resource', 'https://mcp.getdurin.com', production],
        ['mcpIssuer', 'https://auth.getdurin.com', production],
        ['resource', 'https://app.getdurin.com', production],
        ['resource', 'https://moria.mcp.getdurin.com', staging],
        ['mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app', staging],
        ['accountIssuer', 'https://resilient-tiger-64-staging.authkit.app', staging],
        ['accountProvider', 'https://api.workos.com', production],
      ] as const
    ).flatMap(([role, origin, scope]) =>
      [':8443', ':444', ':80', ':1'].map((port) => [role, `${origin}${port}`, scope] as const),
    ),
  )('rejects an allowed %s host on a non-default port: %s', (role, origin, scope) => {
    const problem = untrustedCliHost(role, url(role, origin), scope);
    expect(problem).toContain(new URL(origin).host);
  });

  it.each([
    ['resource', `https://mcp.getdurin.com:443${personal}`],
    ['mcpIssuer', 'https://auth.getdurin.com:443/'],
    ['accountProvider', 'https://api.workos.com:443'],
  ] as const)('accepts an allowed %s host with its default port written out: %s', (role, value) => {
    expect(untrustedCliHost(role, value, production)).toBeUndefined();
  });

  it.each(
    roles([
      'https://evil-durin.com',
      'https://getdurin.com.evil.test',
      'https://mcp.getdurin.com.evil.test',
      'https://auth.getdurin.com.evil.test',
      'https://app.getdurin.com.evil.test',
      'https://mcp-getdurin.com',
      'https://getdurin.co',
      'https://mcp.getdurin.com.',
      'https://xn--getdurn-q9a.com',
    ]),
  )('rejects a lookalike %s host: %s', (role, origin) => {
    expect(untrustedCliHost(role, url(role, origin), production)).toBeDefined();
  });

  it.each(
    roles([
      'https://getdurin.com',
      'https://evil.getdurin.com',
      'https://mcp.app.getdurin.com',
      'https://attacker.mcp.getdurin.com',
      'https://attacker.auth.getdurin.com',
      'https://isengard.getdurin.com',
      'https://api.getdurin.com',
    ]),
  )('rejects a %s subdomain that is not explicitly allowed: %s', (role, origin) => {
    expect(untrustedCliHost(role, url(role, origin), production)).toBeDefined();
  });

  it('does not let the resource and issuer allowlists stand in for each other', () => {
    expect(
      untrustedCliHost('resource', `https://auth.getdurin.com${personal}`, production),
    ).toBeDefined();
    expect(untrustedCliHost('mcpIssuer', 'https://mcp.getdurin.com/', production)).toBeDefined();
  });

  it('keeps production and staging hosts apart', () => {
    expect(
      untrustedCliHost('resource', `https://moria.mcp.getdurin.com${personal}`, production),
    ).toBeDefined();
    expect(
      untrustedCliHost('resource', `https://mcp.getdurin.com${personal}`, staging),
    ).toBeDefined();
    expect(untrustedCliHost('mcpIssuer', 'https://auth.getdurin.com/', staging)).toBeDefined();
    expect(
      untrustedCliHost('mcpIssuer', 'https://resilient-tiger-64-staging.authkit.app/', production),
    ).toBeDefined();
  });

  it.each(
    roles([
      'https://mcp.getdurin.com',
      'https://auth.getdurin.com',
      'https://mcp.durin.acme.test',
      'https://acme.test',
    ]),
  )('gives a deployment without an allowlist only its own host for %s: %s', (role, origin) => {
    expect(untrustedCliHost(role, url(role, origin), custom)).toBeDefined();
  });

  it.each(roles(['https://127.0.0.1:8443', 'https://localhost', 'https://[::1]:8443']))(
    'rejects a loopback %s host outside development: %s',
    (role, origin) => {
      expect(untrustedCliHost(role, url(role, origin), production)).toBeDefined();
      expect(
        untrustedCliHost(role, url(role, origin), { appOrigin: origin, development: false }),
      ).toBeDefined();
    },
  );

  it.each(
    roles([
      'http://127.0.0.1:5272',
      'http://localhost:5272',
      'http://[::1]:5272',
      'https://127.0.0.1:8443',
    ]),
  )('accepts a loopback %s host in development: %s', (role, origin) => {
    expect(untrustedCliHost(role, url(role, origin), local)).toBeUndefined();
  });

  it.each(cliHostRoles)('does not widen non-loopback %s hosts in development', (role) => {
    expect(untrustedCliHost(role, url(role, 'https://evil.test'), local)).toBeDefined();
    expect(
      untrustedCliHost(role, url(role, 'https://evil-durin.com'), {
        ...production,
        development: true,
      }),
    ).toBeDefined();
  });

  it.each([
    ['resource', `https://evil-durin.com${personal}`, 'evil-durin.com', 'mcp.getdurin.com'],
    ['mcpIssuer', 'https://getdurin.com.evil.test/', 'getdurin.com.evil.test', 'auth.getdurin.com'],
  ] as const)(
    'names the unexpected %s host and the expected one in plain language',
    (role, value, unexpected, expected) => {
      const problem = untrustedCliHost(role, value, production);
      expect(problem).toBeTypeOf('string');
      expect(problem).toContain(unexpected);
      expect(problem).toContain(expected);
      expect(problem).toContain('durin');
      expect(problem).not.toMatch(/Durin|https?:\/\/|\/mcp\/u\//u);
    },
  );

  it.each(['not a url', `https://mcp.getdurin.com@evil.test${personal}`])(
    'fails closed for %s',
    (value) => {
      expect(untrustedCliHost('resource', value, production)).toBeDefined();
    },
  );
});

describe('request allowlists', () => {
  it.each([
    [production, ['app.getdurin.com', 'mcp.getdurin.com', 'auth.getdurin.com', 'api.workos.com']],
    [
      staging,
      [
        'moria.app.getdurin.com',
        'moria.mcp.getdurin.com',
        'resilient-tiger-64-staging.authkit.app',
        'api.workos.com',
      ],
    ],
    [custom, ['durin.acme.test']],
    [{ appOrigin: 'https://127.0.0.1:5270', development: false }, []],
  ])('lets setup for %o reach only its app, MCP and account hosts', (scope, hosts) => {
    const allowlist = deploymentHostAllowlist(scope);
    expect(allowlist.hosts.toSorted()).toEqual(hosts.toSorted());
    expect(allowlist.development).toBe(scope.development);
  });

  it('keeps loopback for development in a setup allowlist', () => {
    expect(deploymentHostAllowlist(local)).toMatchObject({ development: true });
  });

  it('limits a saved profile to its MCP endpoint and sign-in server', () => {
    expect(
      profileHostAllowlist({
        resource: `https://MCP.getdurin.com${personal}`,
        mcpIssuer: 'https://auth.getdurin.com/',
        development: false,
      }),
    ).toEqual({ hosts: ['mcp.getdurin.com', 'auth.getdurin.com'], development: false });
  });

  it('keeps the port in a saved profile’s allowlist', () => {
    expect(
      profileHostAllowlist({
        resource: `https://durin.acme.test:8443${personal}`,
        mcpIssuer: 'https://durin.acme.test:443/',
        development: false,
      }).hosts,
    ).toEqual(['durin.acme.test:8443', 'durin.acme.test']);
    expect(
      deploymentHostAllowlist({ appOrigin: 'https://durin.acme.test:8443', development: false })
        .hosts,
    ).toEqual(['durin.acme.test:8443']);
  });
});

describe('account host pinning', () => {
  it.each([production, staging])('accepts the WorkOS account API for $appOrigin', (scope) => {
    for (const value of ['https://api.workos.com', 'https://API.WorkOS.com/'])
      expect(untrustedCliHost('accountProvider', value, scope)).toBeUndefined();
  });

  it.each([
    'https://resilient-tiger-64-staging.authkit.app/',
    'https://Resilient-Tiger-64-Staging.AuthKit.app/',
  ])('accepts the staging account issuer for the staging app: %s', (value) => {
    expect(untrustedCliHost('accountIssuer', value, staging)).toBeUndefined();
  });

  it.each(cliHostRoles)('accepts a %s on the app’s own host', (role) => {
    for (const scope of [production, staging, custom])
      expect(untrustedCliHost(role, url(role, scope.appOrigin), scope)).toBeUndefined();
  });

  it.each(['https://auth.getdurin.com/', 'https://api.workos.com/'])(
    'accepts only the app’s own host as production’s account issuer until it is confirmed: %s',
    (value) => {
      expect(untrustedCliHost('accountIssuer', value, production)).toContain('app.getdurin.com');
    },
  );

  it.each(
    (['accountIssuer', 'accountProvider'] as const).flatMap((role) =>
      [production, staging].flatMap((scope) =>
        [
          'https://api.workos.com.evil.example',
          'https://evil-workos.com',
          'https://workos.com',
          'https://api.workos.co',
          'https://attacker.api.workos.com',
          'https://other-tenant.authkit.app',
          'https://authkit.app',
          'https://resilient-tiger-64-staging.authkit.app.evil.example',
          'https://app.getdurin.com.evil.example',
        ].map((origin) => [role, origin, scope.appOrigin, scope] as const),
      ),
    ),
  )('rejects a lookalike %s host %s for %s', (role, origin, _app, scope) => {
    const problem = untrustedCliHost(role, `${origin}/`, scope);
    expect(problem).toContain(new URL(origin).hostname);
    expect(problem).toContain('durin');
  });

  it('keeps production and staging account issuers apart', () => {
    expect(
      untrustedCliHost(
        'accountIssuer',
        'https://resilient-tiger-64-staging.authkit.app/',
        production,
      ),
    ).toBeDefined();
    expect(untrustedCliHost('accountIssuer', 'https://app.getdurin.com/', staging)).toBeDefined();
  });

  it('gives a deployment without an allowlist only its own host for its account API', () => {
    expect(untrustedCliHost('accountProvider', 'https://api.workos.com', custom)).toBeDefined();
  });

  it.each(['accountIssuer', 'accountProvider'] as const)(
    'accepts a loopback %s host only in development',
    (role) => {
      expect(untrustedCliHost(role, 'http://127.0.0.1:4567', local)).toBeUndefined();
      expect(untrustedCliHost(role, 'https://127.0.0.1:4567', production)).toContain('--dev');
    },
  );

  it.each([
    ['accountIssuer', 'account sign-in issuer'],
    ['accountProvider', 'account sign-in service'],
  ] as const)('names the %s in plain language', (role, name) => {
    const problem = untrustedCliHost(role, 'https://api.workos.com.evil.example/', production);
    expect(problem).toContain(name);
    expect(problem).not.toMatch(/Durin|https?:\/\//u);
  });
});
