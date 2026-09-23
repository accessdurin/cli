import type { CliOptions } from './cli-options.js';
import { CliError } from './cli-options.js';
import { FileProfiles } from './profiles.js';
import { NativeCredentialStore } from './credentials.js';
import { McpOAuthProvider } from './mcp-oauth.js';
import { RemoteMcpClient } from './remote-mcp.js';
import { withProfileLock } from './profile-lock.js';
import { secureFetch } from './safe-fetch.js';

export async function profileConnection(options: CliOptions) {
  const profile = await new FileProfiles(options.directory)
    .load(options.values.profile)
    .catch(() => {
      throw new CliError(
        'No valid organization profile. Run durin in a terminal to complete setup.',
      );
    });
  return {
    profile,
    resource: profile.resource,
    development: profile.development,
    authorization: new McpOAuthProvider({ profile, store: new NativeCredentialStore() }),
    fetch: secureFetch(profile.development),
    serialize: (work: () => Promise<void>) => withProfileLock(options.directory, profile.id, work),
  };
}
export async function withProfileClient<T>(
  options: CliOptions,
  work: (client: RemoteMcpClient) => Promise<T>,
): Promise<T> {
  const connection = await profileConnection(options);
  return withProfileLock(options.directory, connection.profile.id, async () => {
    const client = new RemoteMcpClient(connection);
    try {
      await client.connect();
      return await work(client);
    } finally {
      await client.close();
    }
  });
}
