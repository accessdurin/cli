import { execFile } from 'node:child_process';
import { beforeEach, expect, it, vi } from 'vitest';
import { openBrowser } from './browser.js';

vi.mock('node:child_process', () => ({
  execFile: vi.fn<
    (command: string, args: string[], options: object, callback: (error: null) => void) => void
  >((_command, _args, _options, callback) => callback(null)),
}));
beforeEach(() => {
  vi.mocked(execFile).mockClear();
});

it('hands web URLs to the platform opener', async () => {
  await openBrowser('https://auth.example.test/authorize?state=one');
  expect(vi.mocked(execFile).mock.calls[0]?.[1]).toContain(
    'https://auth.example.test/authorize?state=one',
  );
});

it.each([
  'file:///C:/Windows/System32/calc.exe',
  'smb://attacker.example.test/share',
  'ms-msdt:/id%20PCWDiagnostic',
  'javascript:alert(1)',
  'not a url',
])('never launches the opener for %s', async (url) => {
  await expect(openBrowser(url)).rejects.toThrow('non-web authorization URL');
  expect(execFile).not.toHaveBeenCalled();
});
