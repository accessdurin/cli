import type { CliOptions } from './cli-options.js';
import { withProfileClient } from './profile-client.js';
import { FileProfiles } from './profiles.js';
import { reportHook } from './report-hook.js';
import { flushSpool, spoolDirectory } from './report-spool.js';

/** `durin hooks report`: runs from a harness post-tool hook and never fails the agent. */
export async function reportCommand(options: CliOptions): Promise<void> {
  await reportHook({
    directory: options.directory,
    harness: options.values.harness ?? '',
    cliPath: process.argv[1] ?? '',
    stdin: process.stdin,
  }).catch(() => 'ignored');
}
/** `durin hooks flush`: delivers spooled calls; started detached by the report hook. */
export async function flushCommand(options: CliOptions): Promise<void> {
  const profile = await new FileProfiles(options.directory)
    .load(options.values.profile)
    .catch(() => null);
  if (!profile) return;
  const scoped = { ...options, values: { ...options.values, profile: profile.id } };
  await flushSpool(spoolDirectory(options.directory, profile.id), (work) =>
    withProfileClient(scoped, work),
  ).catch(() => 0);
}
