import {
  Client,
  StreamableHTTPClientTransport,
  type AuthProvider,
  type OAuthClientProvider,
} from '@modelcontextprotocol/client';
import type { CliMcpAccess } from './ports/cli-mcp.js';
import { cliToolCallSchema, type CliToolCall, type CliToolCatalog } from './contracts/cli-mcp.js';
import { mcpToolResultSchema, type McpToolResult } from './contracts/mcp-result.js';
import { mcpToolsListSchema } from './contracts/mcp-discovery.js';
import { safeCliUrl } from './contracts/cli-url.js';

type Options = {
  readonly resource: string;
  readonly authorization: AuthProvider | OAuthClientProvider;
  readonly fetch: typeof fetch;
  readonly development?: boolean;
};

export class RemoteMcpClient implements CliMcpAccess {
  private readonly client = new Client(
    { name: 'Durin CLI', version: '1.0.0' },
    { listMaxPages: 64 },
  );
  private readonly transport: StreamableHTTPClientTransport;

  constructor(options: Options) {
    if (!safeCliUrl(options.resource, options.development ?? false))
      throw new Error('Unsafe MCP resource URL.');
    this.transport = new StreamableHTTPClientTransport(new URL(options.resource), {
      authProvider: options.authorization,
      fetch: options.fetch,
      requestInit: { redirect: 'error' },
      onInsufficientScope: 'throw',
    });
  }

  async connect(): Promise<void> {
    await this.client.connect(this.transport, { timeout: 15_000 });
  }
  async close(): Promise<void> {
    await this.client.close();
  }

  async listTools(): Promise<CliToolCatalog> {
    return mcpToolsListSchema.parse(await this.client.listTools(undefined, { timeout: 15_000 }));
  }

  async callTool(input: CliToolCall): Promise<McpToolResult> {
    const { name, arguments: args, idempotencyKey, approvalId } = cliToolCallSchema.parse(input);
    const control = { idempotencyKey, ...(approvalId ? { approvalId } : {}) };
    const result = await this.client.callTool(
      { name, arguments: args, _meta: { 'io.durin/control': control } },
      { timeout: 60_000 },
    );
    return mcpToolResultSchema.parse(result);
  }
}
