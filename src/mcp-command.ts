import { CliError, type CliOptions } from './cli-options.js';
import { profileConnection, withProfileClient } from './profile-client.js';
import { McpStdioBridge } from './mcp-bridge.js';
import { printJson } from './tool-commands.js';
import { BridgeCalls, FileCallLedger } from './bridge-calls.js';

export async function mcpCommand(options: CliOptions): Promise<void> {
  const connection = await profileConnection(options);
  const action = options.positionals[1];
  if (action === 'endpoint')
    return printJson({
      profile: connection.profile.id,
      resource: connection.resource,
      authentication:
        'Authorize this resource in your MCP client; its grant is separate from the CLI grant.',
    });
  if (action !== 'serve') throw new CliError('Use durin mcp serve or durin mcp endpoint.');
  const bridge = new McpStdioBridge({
    ...connection,
    calls: new BridgeCalls(new FileCallLedger(options.directory, connection.profile.id)),
    onError: (message) => {
      process.stderr.write(`${message}\n`);
    },
  });
  process.once('SIGINT', () => {
    void bridge.close();
  });
  process.once('SIGTERM', () => {
    void bridge.close();
  });
  await bridge.start();
}
export async function verifyProfile(options: CliOptions): Promise<void> {
  const catalog = await withProfileClient(options, (client) => client.listTools());
  printJson({
    verified: true,
    availableTools: catalog.tools.length,
    detail:
      'Authenticated gateway catalog verified. This does not prove the agent harness has been opened.',
  });
}
