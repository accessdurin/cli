import { CliError } from './cli-options.js';
import { randomBytes } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { callbackInput, type CallbackInput } from './oauth-callback-input.js';

type AuthorizationCode = { readonly code: string; readonly issuer?: string };
const ignore = (_value: unknown): void => {};
const deferredCode = () => {
  let resolveCode: (value: AuthorizationCode) => void = ignore;
  let rejectCode: (error: Error) => void = ignore;
  const result = new Promise<AuthorizationCode>((resolve, reject) => {
    resolveCode = resolve;
    rejectCode = reject;
  });
  void result.catch(() => {});
  return {
    result,
    resolve: (value: AuthorizationCode) => resolveCode(value),
    reject: (error: Error) => rejectCode(error),
  };
};
const MESSAGES = {
  code: 'Durin authorization received. Return to your terminal to finish setup.',
  denied: 'Durin authorization was denied. Return to your terminal.',
};
const reply = (response: ServerResponse, status: number, message: string): void => {
  response.writeHead(status, {
    'content-type': 'text/plain; charset=utf-8',
    'cache-control': 'no-store',
    'referrer-policy': 'no-referrer',
    connection: 'close',
  });
  response.end(message);
};

export class LoopbackAuthorization {
  readonly state = randomBytes(32).toString('base64url');
  private readonly pending = deferredCode();
  readonly result = this.pending.result;
  private readonly server = createServer({ maxHeaderSize: 8192 }, (request, response) =>
    this.accept(request, response),
  );
  private timer: ReturnType<typeof setTimeout> | undefined;
  private used = false;
  redirectUrl = '';

  constructor(private readonly issuer: string) {}

  async listen(timeoutMs: number): Promise<void> {
    await new Promise<void>((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(0, '127.0.0.1', resolve);
    });
    const address = this.server.address();
    if (!address || typeof address === 'string')
      throw new CliError('Cannot listen for MCP authorization.');
    this.redirectUrl = `http://127.0.0.1:${address.port}/oauth/callback`;
    this.timer = setTimeout(() => {
      this.pending.reject(new CliError('MCP authorization expired. Run durin to sign in again.'));
      void this.close();
    }, timeoutMs);
  }

  async close(): Promise<void> {
    clearTimeout(this.timer);
    this.pending.reject(new CliError('MCP authorization interrupted. Run durin to sign in again.'));
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => {
      this.server.close(() => resolve());
    });
  }

  private accept(request: IncomingMessage, response: ServerResponse): void {
    const input = callbackInput(request, {
      redirectUrl: this.redirectUrl,
      state: this.state,
      issuer: this.issuer,
    });
    if (!input) return reply(response, 400, 'Invalid authorization callback.');
    if (this.used) return reply(response, 410, 'Authorization already received.');
    this.used = true;
    reply(response, 200, MESSAGES[input.kind]);
    this.finish(input);
    clearTimeout(this.timer);
    this.server.close();
  }

  private finish(input: CallbackInput): void {
    if (input.kind === 'denied')
      return this.pending.reject(new CliError('MCP authorization was denied.'));
    this.pending.resolve({ code: input.code, ...(input.issuer ? { issuer: input.issuer } : {}) });
  }
}

export async function listenForAuthorization(options: {
  issuer: string;
  timeoutMs?: number;
}): Promise<LoopbackAuthorization> {
  const callback = new LoopbackAuthorization(options.issuer);
  await callback.listen(options.timeoutMs ?? 300_000);
  return callback;
}
