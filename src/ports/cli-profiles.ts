import type { CliProfile } from '../contracts/cli-profile.js';

export interface CliProfiles {
  list(): Promise<readonly CliProfile[]>;
  save(profile: CliProfile): Promise<void>;
  load(id?: string): Promise<CliProfile>;
  activate(id: string): Promise<void>;
}
