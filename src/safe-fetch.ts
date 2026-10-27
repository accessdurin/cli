import { CliError } from './cli-options.js';
import { safeCliUrl } from './contracts/cli-url.js';
import { unlistedCliHost, type CliHostAllowlist } from './contracts/cli-host.js';

const destination = (input: string | URL | Request): URL =>
  new URL(input instanceof Request ? input.url : String(input));
/** Every request, wherever metadata or a server sent it, must stay on `allowlist`. */
export const secureFetch =
  (allowlist: CliHostAllowlist, request: typeof fetch = fetch): typeof fetch =>
  async (input, init) => {
    const url = destination(input);
    const base = new URL(url);
    base.search = '';
    if (!safeCliUrl(base.href, allowlist.development))
      throw new CliError('Unsafe authorization or MCP destination.');
    const unlisted = unlistedCliHost(url.href, allowlist);
    if (unlisted) throw new CliError(unlisted);
    const signal = requestSignal(input, init);
    const timeout = AbortSignal.timeout(60_000);
    return request(input, {
      ...init,
      redirect: 'error',
      signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
    });
  };

const requestSignal = (input: string | URL | Request, init?: RequestInit) =>
  init?.signal ?? originalSignal(input);
const originalSignal = (input: string | URL | Request) =>
  input instanceof Request ? input.signal : undefined;
