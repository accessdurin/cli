import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { CliError } from './cli-options.js';

const OPENERS: Readonly<Record<string, (url: string) => readonly [string, string[]]>> = {
  darwin: (url) => ['open', [url]],
  win32: (url) => ['rundll32', ['url.dll,FileProtocolHandler', url]],
};
const linuxOpener = (url: string): readonly [string, string[]] => ['xdg-open', [url]];
const WEB_PROTOCOLS = new Set(['https:', 'http:']);

export const openBrowser = async (url: string): Promise<void> => {
  if (!URL.canParse(url) || !WEB_PROTOCOLS.has(new URL(url).protocol))
    throw new CliError('Refusing to open a non-web authorization URL.');
  const [command, args] = (OPENERS[process.platform] ?? linuxOpener)(url);
  await promisify(execFile)(command, args, { timeout: 5000 }).catch(() => undefined);
};
