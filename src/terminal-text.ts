import { format, stripVTControlCharacters } from 'node:util';

const CONTROL_OR_BIDI = /[\p{Cc}\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;
/** Server-supplied text is displayed inert: no escape sequences, controls, or bidi overrides. */
export const terminalText = (value: string): string =>
  stripVTControlCharacters(value).replace(CONTROL_OR_BIDI, '');

const CONTROL_OR_BIDI_EXCEPT_LAYOUT =
  /[^\P{Cc}\n\t]|[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;
const CONSOLE_METHODS = ['debug', 'error', 'info', 'log', 'warn'] as const;
/** Library diagnostics can quote server responses, so console output keeps only line layout. */
export const sanitizeConsole = (target: Console): void => {
  for (const method of CONSOLE_METHODS) {
    const write = target[method].bind(target);
    target[method] = (...values: unknown[]) => {
      write(stripVTControlCharacters(format(...values)).replace(CONTROL_OR_BIDI_EXCEPT_LAYOUT, ''));
    };
  }
};

const UNESCAPED_JSON_CONTROL = /[\u007f-\u009f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;
/** JSON.stringify escapes C0 controls but leaves DEL, C1, and bidi controls raw. */
export const terminalJson = (value: unknown): string =>
  JSON.stringify(value, null, 2).replace(
    UNESCAPED_JSON_CONTROL,
    (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, '0')}`,
  );
