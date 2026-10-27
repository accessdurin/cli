import { describe, expect, it } from 'vitest';
import { untrustedCliHost, type CliHostRole } from './cli-host.js';

const personal = `/mcp/u/${'a'.repeat(64)}`;
const production = { appOrigin: 'https://app.getdurin.com', development: false };
const staging = { appOrigin: 'https://moria.app.getdurin.com', development: false };
const custom = { appOrigin: 'https://durin.acme.test', development: false };
const local = { appOrigin: 'http://127.0.0.1:5270', development: true };
const url = (role: CliHostRole, origin: string) =>
  role === 'resource' ? `${origin}${personal}` : `${origin}/`;
const roles = (origins: readonly string[]) =>
  (['resource', 'mcpIssuer'] as const).flatMap((role) =>
    origins.map((origin) => [role, origin] as const),
  );

describe('MCP host pinning', () => {
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

  it.each(['resource', 'mcpIssuer'] as const)(
    'accepts a %s on the app origin host for a deployment without an allowlist',
    (role) => {
      expect(untrustedCliHost(role, url(role, 'https://durin.acme.test'), custom)).toBeUndefined();
      expect(
        untrustedCliHost(role, url(role, 'https://durin.acme.test:8443'), custom),
      ).toBeUndefined();
    },
  );

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

  it.each(['resource', 'mcpIssuer'] as const)(
    'does not widen non-loopback %s hosts in development',
    (role) => {
      expect(untrustedCliHost(role, url(role, 'https://evil.test'), local)).toBeDefined();
      expect(
        untrustedCliHost(role, url(role, 'https://evil-durin.com'), {
          ...production,
          development: true,
        }),
      ).toBeDefined();
    },
  );

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
