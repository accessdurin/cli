import { CliError, type CliOptions } from './cli-options.js';
import { FileProfiles } from './profiles.js';
import { NativeCredentialStore } from './credentials.js';
import { retryPendingRevocations, signOutMcp } from './logout.js';
import { withProfileLock } from './profile-lock.js';
import { FileRevocationMarker } from './revocation-marker.js';
import { printJson } from './tool-commands.js';

export async function profileCommand(options: CliOptions): Promise<void> {
  const profiles = new FileProfiles(options.directory);
  const action = options.positionals[1];
  if (action === 'list') return printJson(await profiles.list());
  if (action !== 'use') throw new CliError('Use durin profiles list or durin profiles use <id>.');
  const id = options.positionals[2];
  if (!id) throw new CliError('Provide a profile ID from durin profiles list.');
  await profiles.activate(id);
  printJson({ activeProfile: id });
}
export async function logoutCommand(options: CliOptions): Promise<void> {
  const profile = await new FileProfiles(options.directory).load(options.values.profile);
  const store = new NativeCredentialStore();
  await retryPendingRevocations(options.directory, profile, store);
  const { remote, local } = await withProfileLock(options.directory, profile.id, () =>
    signOutMcp({
      profile,
      store,
      marker: new FileRevocationMarker(options.directory, profile.id),
      request: fetch,
      force: options.values.force ?? false,
    }),
  );
  printJson({
    profile: profile.id,
    localMcpGrant: local,
    remoteMcpGrant: remote,
    detail:
      local === 'retained'
        ? 'The authorization server could not confirm that access was revoked, so sign-out hasn’t finished. Until it does, MCP calls and durin tools are blocked for this profile. Run durin logout again to retry, or durin logout --force to sign out on this computer anyway and lift the block.'
        : 'Agent configuration is retained. Run durin to sign in again. Other organization MCP grants are unchanged.',
  });
  if (remote === 'failed') process.exitCode = 1;
}
