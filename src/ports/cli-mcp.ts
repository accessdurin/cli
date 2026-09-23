import type { CliToolCall, CliToolCatalog } from '../contracts/cli-mcp.js';
import type { McpToolResult } from '../contracts/mcp-result.js';

export interface CliMcpAccess {
  connect(): Promise<void>;
  listTools(): Promise<CliToolCatalog>;
  callTool(input: CliToolCall): Promise<McpToolResult>;
  close(): Promise<void>;
}
