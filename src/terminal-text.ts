import { stripVTControlCharacters } from 'node:util';

const CONTROL_OR_BIDI = /[\p{Cc}\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/gu;
/** Server-supplied text is displayed inert: no escape sequences, controls, or bidi overrides. */
export const terminalText = (value: string): string =>
  stripVTControlCharacters(value).replace(CONTROL_OR_BIDI, '');

const UNESCAPED_JSON_CONTROL = /[\u007f-\u009f]/gu;
/** JSON.stringify escapes C0 controls but leaves DEL and C1 controls raw. */
export const terminalJson = (value: unknown): string =>
  JSON.stringify(value, null, 2).replace(
    UNESCAPED_JSON_CONTROL,
    (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, '0')}`,
  );
