import type { CliOptions } from './cli-options.js';
import { withProfileClient } from './profile-client.js';
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
  const spool = spoolDirectory(options.directory, options.values.profile ?? '');
  await flushSpool(spool, (work) => withProfileClient(options, work)).catch(() => 0);
}
