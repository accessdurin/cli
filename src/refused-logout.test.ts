import { expect, it } from 'vitest';
import { pinnedHttpsHost } from './refused-logout.js';

it.each([
  ['https://auth.getdurin.com', true],
  ['http://auth.getdurin.com', false],
  ['https://user@auth.getdurin.com', false],
  ['https://user:secret@auth.getdurin.com/oauth2/revoke', false],
  ['https://auth.getdurin.com:443', false],
  ['https://auth.getdurin.com:443/oauth2/revoke', false],
  ['https://auth.getdurin.com:8443', false],
  ['https://AUTH.getdurin.com', false],
  ['https://auth.getdurin.com.', false],
  ['https://auth.getdurin.com.evil.com', false],
  ['https://evilauth.getdurin.com', false],
  ['https://evil.example', false],
])('pinnedHttpsHost accepts only the pinned https host (%s → %s)', (endpoint, accepted) => {
  expect(pinnedHttpsHost(endpoint)).toBe(accepted);
});
