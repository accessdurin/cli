import { expect, it } from 'vitest';
import { sanitizeConsole, terminalJson, terminalText } from './terminal-text.js';

const CONTROL = /[\p{Cc}\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/u;

it.each([
  ['clipboard write', 'Acme\x1b]52;c;ZWNobyBwd25lZA==\x07 Corp'],
  ['base64 clipboard write', 'Acme\x1b]52;c;a+b/c==\x07 Corp'],
  ['line rewrite', 'Acme\x1b[1A\x1b[2K Corp'],
  ['hyperlink', 'Acme\x1b]8;;https://attacker.example.test\x07 Corp\x1b]8;;\x07'],
  ['8-bit CSI', 'Acme\u009b2J Corp'],
  ['bidi override', 'Acme\u202e Corp'],
  ['line break', 'Acme\r\n Corp'],
])('renders server text without a %s', (_name, value) => {
  const rendered = terminalText(value);
  expect(rendered).not.toMatch(CONTROL);
  expect(rendered.startsWith('Acme')).toBe(true);
  expect(rendered.endsWith(' Corp')).toBe(true);
});

it('keeps ordinary international names unchanged', () => {
  expect(terminalText('Café Ünïcode 株式会社 שלום 👩\u200d💻')).toBe(
    'Café Ünïcode 株式会社 שלום 👩\u200d💻',
  );
});

it('prints JSON that round-trips without raw control or bidi characters', () => {
  const value = { text: 'a\u001b[2Jb\u009b2Jc\u007fd\u0085e\u202ef\u2066g', name: 'Café' };
  const printed = terminalJson(value);
  expect(printed.replaceAll('\n', '')).not.toMatch(CONTROL);
  expect(JSON.parse(printed)).toEqual(value);
});

it('keeps console line layout while removing control sequences', () => {
  const written: string[] = [];
  const target = Object.fromEntries(
    ['debug', 'error', 'info', 'log', 'warn'].map((method) => [
      method,
      (text: string) => written.push(`${method}:${text}`),
    ]),
  ) as unknown as Console;
  sanitizeConsole(target);
  target.warn('[mcp-sdk] Cause: %s', '"\u009b2J\u001b]52;c;eA==\u0007\u202ebad"\n\tnext');
  target.log({ text: 'ok\u001b[31m' });
  expect(written).toEqual([`warn:[mcp-sdk] Cause: "bad"\n\tnext`, "log:{ text: 'ok\\x1B[31m' }"]);
});
