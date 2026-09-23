import { Client } from '@modelcontextprotocol/client';
import { StdioClientTransport, getDefaultEnvironment } from '@modelcontextprotocol/client/stdio';
import { mcpToolsListSchema } from './contracts/mcp-discovery.js';
import type { AgentConfig } from './agents.js';
import { updatedBridgeConfig, type BridgeConfiguration } from './bridge-config.js';
import { readConfig } from './config.js';
import { CliError } from './cli-options.js';

export async function verifySavedBridge(
  agent: AgentConfig,
  config: BridgeConfiguration,
): Promise<number> {
  const raw = await readConfig(agent.configPath);
  if (raw === null || updatedBridgeConfig(raw, agent, config) !== raw)
    throw new CliError(
      'The saved agent configuration is missing its organization bridge. Run durin to resume.',
    );
  const transport = new StdioClientTransport({
    command: config.nodePath,
    args: [
      config.cliPath,
      'mcp',
      'serve',
      '--profile',
      config.profileId,
      '--config-dir',
      config.configDirectory,
    ],
    stderr: 'inherit',
    env: bridgeEnvironment(),
  });
  const client = new Client({ name: 'Durin setup verification', version: '1.0.0' });
  try {
    await client.connect(transport, { timeout: 15_000 });
    return mcpToolsListSchema.parse(await client.listTools(undefined, { timeout: 15_000 })).tools
      .length;
  } finally {
    await client.close();
  }
}

const bridgeEnvironment = (): Record<string, string> => ({
  ...getDefaultEnvironment(),
  ...(process.env.NODE_EXTRA_CA_CERTS
    ? { NODE_EXTRA_CA_CERTS: process.env.NODE_EXTRA_CA_CERTS }
    : {}),
});
