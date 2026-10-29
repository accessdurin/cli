import type { CliProfile } from '../contracts/cli-profile.js';

export type RefusedProfile = { readonly id: string; readonly reason: string };
export type ProfileList = {
  readonly profiles: readonly CliProfile[];
  readonly refused: readonly RefusedProfile[];
};
export interface CliProfiles {
  list(): Promise<ProfileList>;
  save(profile: CliProfile): Promise<void>;
  load(id?: string): Promise<CliProfile>;
  activate(id: string): Promise<void>;
}
