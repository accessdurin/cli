import { describe, expect, it } from 'vitest';
import { loopbackCliHost, safeCliUrl } from './cli-url.js';
import { deploymentHostAllowlist, untrustedCliHost } from './cli-host.js';
import { cliProfileScopeSchema } from './cli-profile.js';

const personal = `/mcp/u/${'a'.repeat(64)}`;
const production = { appOrigin: 'https://app.getdurin.com', development: false };
/** Each loopback form, as a URL host would be written. */
const loopbackOrigins = [
  'https://localhost',
  'https://LOCALHOST',
  'https://localhost.',
  'https://LocalHost.',
  'https://dev.localhost',
  'https://127.0.0.1',
  'https://127.0.0.2',
  'https://127.255.255.254',
  'https://127.1',
  'https://0x7f.1',
  'https://2130706433',
  'https://127.0.0.1.',
  'https://0.0.0.0',
  'https://0',
  'https://[::1]',
  'https://[0:0:0:0:0:0:0:1]',
  'https://[::]',
  'https://[::ffff:127.0.0.1]',
  'https://[::FFFF:127.1.2.3]',
  'https://[::ffff:7f00:1]',
  'https://[0:0:0:0:0:ffff:7f00:1]',
  'https://[::ffff:7fff:ffff]',
  'https://[::ffff:0.0.0.0]',
];

describe('loopback hosts', () => {
  it.each([
    'localhost',
    'LOCALHOST',
    'localhost.',
    'dev.localhost',
    '127.0.0.1',
    '127.0.0.2',
    '127.255.255.255',
    '127.1',
    '127.0.0.1.',
    '0.0.0.0',
    '[::1]',
    '::1',
    '[0:0:0:0:0:0:0:1]',
    '[::]',
    '[::ffff:127.0.0.1]',
    '::ffff:127.0.0.1',
    '[::FFFF:127.9.8.7]',
    '[::ffff:7f00:1]',
    '[0:0:0:0:0:ffff:7f00:1]',
    '[::ffff:0:0]',
  ])('treats %s as loopback', (host) => {
    expect(loopbackCliHost(host)).toBe(true);
  });

  it.each([
    'localhost.evil.com',
    'localhost..',
    'localhost.com',
    'evil-localhost',
    'mylocalhost',
    '127.0.0.1.evil.com',
    '128.0.0.1',
    '126.255.255.255',
    '10.0.0.1',
    '[::2]',
    '[::ffff:128.0.0.1]',
    '[::ffff:10.0.0.1]',
    '[64:ff9b::7f00:1]',
    'mcp.getdurin.com',
    '',
  ])('does not treat %j as loopback', (host) => {
    expect(loopbackCliHost(host)).toBe(false);
  });

  it.each(loopbackOrigins)('needs --dev for an MCP endpoint on %s', (origin) => {
    const value = `${origin}${personal}`;
    expect(untrustedCliHost('resource', value, production)).toContain('--dev');
    expect(untrustedCliHost('resource', value, { ...production, development: true })).toBe(
      undefined,
    );
  });

  it.each(loopbackOrigins)('needs --dev for an --app-url of %s that serves itself', (origin) => {
    const appOrigin = new URL(origin).origin;
    for (const role of ['resource', 'mcpIssuer', 'accountIssuer', 'accountProvider'] as const) {
      const value = role === 'resource' ? `${appOrigin}${personal}` : `${appOrigin}/`;
      expect(untrustedCliHost(role, value, { appOrigin, development: false })).toContain('--dev');
      expect(untrustedCliHost(role, value, { appOrigin, development: true })).toBeUndefined();
    }
    expect(deploymentHostAllowlist({ appOrigin, development: false }).hosts).toEqual([]);
  });

  it.each(loopbackOrigins)('saves a profile on %s only with --dev', (origin) => {
    const appOrigin = new URL(origin).origin;
    const scope = {
      appOrigin,
      accountIssuer: `${appOrigin}/`,
      accountClientId: 'client_account',
      principalId: 'user_one',
      workosOrganizationId: 'org_provider',
      organizationId: 'org_one',
      environment: 'production' as const,
      resource: `${appOrigin}${personal}`,
      mcpIssuer: `${appOrigin}/`,
    };
    expect(cliProfileScopeSchema.safeParse({ ...scope, development: false }).success).toBe(false);
    expect(cliProfileScopeSchema.safeParse({ ...scope, development: true }).success).toBe(true);
  });

  it.each(loopbackOrigins)('allows HTTP on %s only with --dev', (origin) => {
    const value = `${origin.replace('https:', 'http:')}:5272/`;
    expect(safeCliUrl(value, false)).toBe(false);
    expect(safeCliUrl(value, true)).toBe(true);
  });
});
