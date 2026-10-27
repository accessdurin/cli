import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { FileProfiles, createProfile } from './profiles.js';

const scope = {
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://accounts.example.test/',
  accountClientId: 'client_account',
  principalId: 'user_one',
  workosOrganizationId: 'org_provider_one',
  organizationId: 'org_one',
  environment: 'production' as const,
  resource: `https://app.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://app.example.test/',
  development: false,
};
const directories: string[] = [];
afterEach(async () => {
  await Promise.all(
    directories.splice(0).map((path) => rm(path, { recursive: true, force: true })),
  );
});

describe('organization profiles', () => {
  it('keeps an explicit organization profile pinned after the active terminal profile changes', async () => {
    const directory = await mkdtemp(join(tmpdir(), 'durin-profiles-'));
    directories.push(directory);
    const profiles = new FileProfiles(directory);
    const first = createProfile(scope);
    const second = createProfile({
      ...scope,
      organizationId: 'org_two',
      workosOrganizationId: 'org_provider_two',
      resource: `https://app.example.test/mcp/u/${'b'.repeat(64)}`,
    });
    await profiles.save(first);
    await profiles.save(second);
    await profiles.activate(first.id);
    expect(await profiles.load()).toEqual(first);
    await profiles.activate(second.id);
    expect(await profiles.load()).toEqual(second);
    expect(await profiles.load(first.id)).toEqual(first);
    const saved = await readFile(join(directory, 'profiles', `${first.id}.json`), 'utf8');
    expect(JSON.parse(saved)).toEqual(first);
  });
  it.each(['principalId', 'organizationId', 'mcpIssuer', 'resource'] as const)(
    'rejects changed %s metadata before loading a grant',
    async (field) => {
      const directory = await mkdtemp(join(tmpdir(), 'durin-profiles-'));
      directories.push(directory);
      const profiles = new FileProfiles(directory);
      const profile = createProfile(scope);
      await profiles.save(profile);
      const tampered = { ...profile, [field]: `${profile[field]}-changed` };
      await writeFile(join(directory, 'profiles', `${profile.id}.json`), JSON.stringify(tampered));
      await expect(profiles.load(profile.id)).rejects.toThrow('profile binding');
    },
  );
  it.each([
    { appOrigin: 'http://app.example.test' },
    { appOrigin: 'http://127.0.0.1:5270' },
    { mcpIssuer: 'https://user:password@app.example.test/' },
    { resource: 'https://app.example.test/mcp/u/incorrect' },
    { development: true, resource: `http://app.example.test/mcp/u/${'a'.repeat(64)}` },
  ])('rejects insecure or unbound profile endpoints: %j', (changes) => {
    expect(() => createProfile({ ...scope, ...changes })).toThrow();
  });
});

describe('organization profile MCP hosts', () => {
  const personal = `/mcp/u/${'a'.repeat(64)}`;
  const production = {
    ...scope,
    appOrigin: 'https://app.getdurin.com',
    accountIssuer: 'https://auth.getdurin.com/',
    resource: `https://mcp.getdurin.com${personal}`,
    mcpIssuer: 'https://auth.getdurin.com/',
  };
  const local = {
    ...scope,
    appOrigin: 'http://127.0.0.1:5270',
    accountIssuer: 'http://127.0.0.1:5270/',
    resource: `http://127.0.0.1:5272${personal}`,
    mcpIssuer: 'http://127.0.0.1:5272/',
    development: true,
  };
  const bindingId = (value: object) =>
    createHash('sha256').update(JSON.stringify(value)).digest('hex');

  it.each([
    ['production', production],
    ['same-host', scope],
    ['local development', local],
  ])('accepts a %s profile', (_name, value) => {
    expect(createProfile(value)).toMatchObject(value);
  });

  it.each([
    { resource: `https://evil-durin.com${personal}` },
    { resource: `https://getdurin.com.evil.test${personal}` },
    { resource: `https://evil.getdurin.com${personal}` },
    { resource: `https://moria.mcp.getdurin.com${personal}` },
    { resource: `https://127.0.0.1:8443${personal}` },
    { mcpIssuer: 'https://evil-durin.com/' },
    { mcpIssuer: 'https://getdurin.com.evil.test/' },
    { mcpIssuer: 'https://evil.getdurin.com/' },
    { mcpIssuer: 'https://mcp.getdurin.com/' },
    { mcpIssuer: 'https://localhost/' },
  ])('rejects a production profile with an MCP host it does not use: %j', (changes) => {
    expect(() => createProfile({ ...production, ...changes })).toThrow(
      new URL(Object.values(changes)[0]!).hostname,
    );
  });

  it('rejects loopback MCP hosts unless the profile is for development', () => {
    expect(() => createProfile({ ...local, development: false })).toThrow();
    expect(() =>
      createProfile({
        ...local,
        appOrigin: 'https://127.0.0.1:8443',
        accountIssuer: 'https://127.0.0.1:8443/',
        resource: `https://127.0.0.1:8443${personal}`,
        mcpIssuer: 'https://127.0.0.1:8443/',
        development: false,
      }),
    ).toThrow('127.0.0.1');
  });

  it.each([
    { resource: `https://evil-durin.com${personal}` },
    { mcpIssuer: 'https://getdurin.com.evil.test/' },
  ])(
    'refuses to load a saved profile whose binding is intact but whose MCP host is not used: %j',
    async (changes) => {
      const directory = await mkdtemp(join(tmpdir(), 'durin-profiles-'));
      directories.push(directory);
      const saved = { ...production, ...changes };
      const id = bindingId(saved);
      await mkdir(join(directory, 'profiles'), { recursive: true });
      await writeFile(
        join(directory, 'profiles', `${id}.json`),
        JSON.stringify({ ...saved, version: 1, id }),
      );
      await writeFile(join(directory, 'active'), `${id}\n`);
      const host = new URL(Object.values(changes)[0]!).hostname;
      await expect(new FileProfiles(directory).load(id)).rejects.toThrow(host);
      await expect(new FileProfiles(directory).load()).rejects.toThrow(host);
      await expect(new FileProfiles(directory).list()).rejects.toThrow(host);
    },
  );
});
