import type { Readable, Writable } from 'node:stream';
import {
  StreamableHTTPClientTransport,
  isJSONRPCRequest,
  UnauthorizedError,
  type AuthProvider,
  type OAuthClientProvider,
  type JSONRPCMessage,
} from '@modelcontextprotocol/client';
import { StdioServerTransport } from '@modelcontextprotocol/server/stdio';
import { cliMcpNegotiationSchema } from './contracts/cli-mcp.js';
import { safeCliUrl } from './contracts/cli-url.js';
import type { BridgeCalls } from './bridge-calls.js';
import { CliError } from './cli-options.js';

type Options = {
  readonly resource: string;
  readonly authorization: AuthProvider | OAuthClientProvider;
  readonly fetch: typeof fetch;
  readonly input?: Readable;
  readonly output?: Writable;
  readonly development?: boolean;
  readonly calls?: BridgeCalls;
  readonly serialize?: (work: () => Promise<void>) => Promise<void>;
  readonly onError?: (message: string) => void;
};
const failureMessage = (error: unknown): string => {
  if (error instanceof CliError) return error.message;
  return error instanceof UnauthorizedError
    ? 'Durin MCP sign-in is required. Run durin to authorize this organization.'
    : 'Durin MCP request failed. Its outcome may be uncertain; retain the same idempotency key when retrying.';
};

export class McpStdioBridge {
  private readonly local: StdioServerTransport;
  private readonly remote: StreamableHTTPClientTransport;
  private initializedRequest: string | number | undefined;
  private closed = false;
  private delivery: Promise<void> = Promise.resolve();

  constructor(private readonly options: Options) {
    if (!safeCliUrl(options.resource, options.development ?? false))
      throw new Error('Unsafe MCP resource URL.');
    this.local = new StdioServerTransport(options.input, options.output, {
      maxBufferSize: 1_048_576,
    });
    this.remote = new StreamableHTTPClientTransport(new URL(options.resource), {
      authProvider: options.authorization,
      fetch: options.fetch,
      requestInit: { redirect: 'error' },
      onInsufficientScope: 'throw',
    });
    Object.assign(this.local, {
      onmessage: (message: JSONRPCMessage) => {
        void this.forward(message).catch(() => this.close());
      },
      onerror: () => {
        this.report('Invalid MCP input.');
        void this.close();
      },
      onclose: () => {
        void this.close();
      },
    });
    Object.assign(this.remote, {
      onmessage: (message: JSONRPCMessage) => {
        this.negotiated(message);
        this.delivery = this.deliver(message);
        void this.delivery.catch(() => this.close());
      },
      onerror: () => this.report('Durin MCP transport failed.'),
      onclose: () => {
        void this.close();
      },
    });
  }

  async start(): Promise<void> {
    await this.remote.start();
    await this.local.start();
  }
  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    await Promise.all([this.remote.close(), this.local.close()]);
  }

  private async forward(message: JSONRPCMessage): Promise<void> {
    if (isJSONRPCRequest(message) && message.method === 'initialize')
      this.initializedRequest = message.id;
    try {
      await this.serialized(() => this.send(message));
    } catch (error) {
      await this.failed(message, error);
    }
  }
  private async send(message: JSONRPCMessage): Promise<void> {
    const prepared = this.options.calls ? await this.options.calls.outgoing(message) : message;
    await this.remote.send(prepared);
    await this.delivery;
  }
  private deliver(message: JSONRPCMessage): Promise<void> {
    const send = (value: JSONRPCMessage) => this.local.send(value);
    return this.options.calls ? this.options.calls.deliver(message, send) : send(message);
  }
  private serialized(work: () => Promise<void>): Promise<void> {
    return this.options.serialize ? this.options.serialize(work) : work();
  }
  private async failed(message: JSONRPCMessage, error: unknown): Promise<void> {
    if (!isJSONRPCRequest(message)) return this.report(failureMessage(error));
    await this.local.send({
      jsonrpc: '2.0',
      id: message.id,
      error: { code: -32000, message: failureMessage(error) },
    });
  }
  private negotiated(message: JSONRPCMessage): void {
    const result = cliMcpNegotiationSchema.safeParse(message);
    if (result.success && result.data.id === this.initializedRequest)
      this.remote.setProtocolVersion(result.data.result.protocolVersion);
  }
  private report(message: string): void {
    this.options.onError?.(message);
  }
}
