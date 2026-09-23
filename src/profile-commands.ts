import { CliError, type CliOptions } from './cli-options.js';
import { FileProfiles } from './profiles.js';
import { NativeCredentialStore } from './credentials.js';
import { signOutMcp } from './logout.js';
import { withProfileLock } from './profile-lock.js';
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
  const remote = await withProfileLock(options.directory, profile.id, () =>
    signOutMcp({ profile, store: new NativeCredentialStore(), request: fetch }),
  );
  printJson({
    profile: profile.id,
    localMcpGrant: 'cleared',
    remoteMcpGrant: remote,
    detail:
      'Agent configuration is retained. Run durin to sign in again. Other organization MCP grants are unchanged.',
  });
  if (remote === 'failed') process.exitCode = 1;
}
