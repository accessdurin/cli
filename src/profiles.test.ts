import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
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
  resource: `https://mcp.example.test/mcp/u/${'a'.repeat(64)}`,
  mcpIssuer: 'https://auth.example.test/',
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
      resource: `https://mcp.example.test/mcp/u/${'b'.repeat(64)}`,
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
    { mcpIssuer: 'https://user:password@auth.example.test/' },
    { resource: 'https://mcp.example.test/mcp/u/incorrect' },
    { development: true, resource: `http://mcp.example.test/mcp/u/${'a'.repeat(64)}` },
  ])('rejects insecure or unbound profile endpoints: %j', (changes) => {
    expect(() => createProfile({ ...scope, ...changes })).toThrow();
  });
});
