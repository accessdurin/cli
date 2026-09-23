import { timingSafeEqual } from 'node:crypto';
import type { IncomingMessage } from 'node:http';
import { authorizationCallbackSchema } from './contracts/cli-auth.js';

export type CallbackInput = ReturnType<typeof authorizationCallbackSchema.parse>;
type Binding = { redirectUrl: string; state: string; issuer: string };
const sameState = (left: string, right: string): boolean => {
  const a = Buffer.from(left),
    b = Buffer.from(right);
  return a.length === b.length && timingSafeEqual(a, b);
};
const matches = (input: CallbackInput, binding: Binding): boolean =>
  [
    sameState(input.state, binding.state),
    input.issuer === undefined || input.issuer === binding.issuer,
  ].every(Boolean);
const parseBoundInput = (url: URL, binding: Binding): CallbackInput | null => {
  const input = authorizationCallbackSchema.safeParse(Object.fromEntries(url.searchParams));
  if (!input.success) return null;
  return matches(input.data, binding) ? input.data : null;
};

export function callbackInput(request: IncomingMessage, binding: Binding): CallbackInput | null {
  const target = request.url ?? '/';
  if (!URL.canParse(target, binding.redirectUrl)) return null;
  const url = new URL(target, binding.redirectUrl);
  const expected = new URL(binding.redirectUrl);
  const valid = [
    request.method === 'GET',
    request.headers.host === expected.host,
    url.origin === expected.origin,
    url.pathname === expected.pathname,
    ['code', 'state', 'iss', 'error'].every((key) => url.searchParams.getAll(key).length <= 1),
  ];
  return valid.every(Boolean) ? parseBoundInput(url, binding) : null;
}
