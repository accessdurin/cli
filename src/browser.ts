import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const OPENERS: Readonly<Record<string, (url: string) => readonly [string, string[]]>> = {
  darwin: (url) => ['open', [url]],
  win32: (url) => ['rundll32', ['url.dll,FileProtocolHandler', url]],
};
const linuxOpener = (url: string): readonly [string, string[]] => ['xdg-open', [url]];

export const openBrowser = async (url: string): Promise<void> => {
  const [command, args] = (OPENERS[process.platform] ?? linuxOpener)(url);
  await promisify(execFile)(command, args, { timeout: 5000 }).catch(() => undefined);
};
