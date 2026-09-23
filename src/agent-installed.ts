import { accessSync, constants, existsSync } from 'node:fs';
import { delimiter, join } from 'node:path';
import { homedir } from 'node:os';

const MAC_APPS: Readonly<Record<string, string>> = {
  codex: 'Codex.app',
  cursor: 'Cursor.app',
  windsurf: 'Windsurf.app',
};
const executable = (path: string): boolean => {
  try {
    accessSync(path, constants.X_OK);
    return true;
  } catch {
    return false;
  }
};
const appInstalled = (key: string, platform: string): boolean => {
  if (platform !== 'darwin') return false;
  const app = MAC_APPS[key];
  return Boolean(
    app &&
      ['/Applications', join(homedir(), 'Applications')].some((directory) =>
        existsSync(join(directory, app)),
      ),
  );
};
function executableInstalled(key: string, env: NodeJS.ProcessEnv, platform: string): boolean {
  const extensions = platform === 'win32' ? ['', '.exe', '.cmd', '.bat'] : [''];
  const paths = (env.PATH ?? '').split(delimiter).filter(Boolean);
  return paths.some((directory) =>
    extensions.some((extension) => executable(join(directory, `${key}${extension}`))),
  );
}
export const agentInstalled = (
  key: string,
  env: NodeJS.ProcessEnv = process.env,
  platform: string = process.platform,
): boolean => executableInstalled(key, env, platform) || appInstalled(key, platform);
