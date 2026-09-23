import { CliError } from './cli-options.js';
import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { z } from 'zod';
import type { CliProfiles } from './ports/cli-profiles.js';
import {
  cliProfileIdSchema,
  cliProfileSchema,
  cliProfileScopeSchema,
  type CliProfile,
  type CliProfileScope,
} from './contracts/cli-profile.js';

export function createProfile(input: CliProfileScope): CliProfile {
  const scope = cliProfileScopeSchema.parse(input);
  const id = createHash('sha256').update(JSON.stringify(scope)).digest('hex');
  return { ...scope, version: 1, id };
}
const parseProfile = (value: unknown): CliProfile => {
  const parsed = cliProfileSchema.safeParse(value);
  if (!parsed.success) throw new CliError('Invalid Durin profile binding.');
  const { id, version: _version, ...scope } = parsed.data;
  const expected = createProfile(scope);
  if (id !== expected.id)
    throw new CliError('Invalid Durin profile binding. Run durin to configure it again.');
  return expected;
};

export class FileProfiles implements CliProfiles {
  constructor(private readonly directory: string) {}

  async save(profile: CliProfile): Promise<void> {
    const parsed = parseProfile(profile);
    await atomicWrite(this.path(parsed.id), `${JSON.stringify(parsed, null, 2)}\n`);
  }

  async load(id?: string): Promise<CliProfile> {
    const selected = id ?? (await readFile(join(this.directory, 'active'), 'utf8')).trim();
    const profile = parseProfile(JSON.parse(await readFile(this.path(selected), 'utf8')));
    if (profile.id !== selected) throw new CliError('Invalid Durin profile binding.');
    return profile;
  }

  async list(): Promise<readonly CliProfile[]> {
    const names = await readdir(join(this.directory, 'profiles')).catch((error: unknown) => {
      if (z.object({ code: z.literal('ENOENT') }).safeParse(error).success) return [];
      throw error;
    });
    const ids = names
      .filter((name) => /^[a-f0-9]{64}\.json$/u.test(name))
      .map((name) => name.slice(0, -5));
    return Promise.all(ids.map((id) => this.load(id)));
  }

  async activate(id: string): Promise<void> {
    const profile = await this.load(id);
    await atomicWrite(join(this.directory, 'active'), `${profile.id}\n`);
  }

  private path(id: string): string {
    return join(this.directory, 'profiles', `${cliProfileIdSchema.parse(id)}.json`);
  }
}

async function atomicWrite(path: string, value: string): Promise<void> {
  await mkdir(dirname(path), { recursive: true, mode: 0o700 });
  const temporary = `${path}.${randomUUID()}.tmp`;
  try {
    await writeFile(temporary, value, { mode: 0o600, flag: 'wx' });
    await rename(temporary, path);
  } finally {
    await rm(temporary, { force: true });
  }
}
