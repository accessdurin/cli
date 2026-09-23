import { expect, it } from 'vitest';
import { listenForAuthorization } from './oauth-callback.js';

it('accepts one loopback authorization code only after validating state and issuer', async () => {
  const callback = await listenForAuthorization({ issuer: 'https://auth.example.test/' });
  try {
    const wrong = new URL(callback.redirectUrl);
    wrong.search = new URLSearchParams({
      code: 'synthetic-code',
      state: 'wrong',
      iss: 'https://auth.example.test/',
    }).toString();
    expect((await fetch(wrong)).status).toBe(400);
    const valid = new URL(callback.redirectUrl);
    valid.search = new URLSearchParams({
      code: 'synthetic-code',
      state: callback.state,
      iss: 'https://auth.example.test/',
    }).toString();
    expect((await fetch(valid)).status).toBe(200);
    expect(await callback.result).toEqual({
      code: 'synthetic-code',
      issuer: 'https://auth.example.test/',
    });
    await expect(fetch(valid)).rejects.toThrow();
  } finally {
    await callback.close();
  }
});

it('stops immediately when the user denies the matching authorization transaction', async () => {
  const callback = await listenForAuthorization({ issuer: 'https://auth.example.test/' });
  try {
    const url = new URL(callback.redirectUrl);
    url.search = new URLSearchParams({ error: 'access_denied', state: callback.state }).toString();
    expect((await fetch(url)).status).toBe(200);
    await expect(callback.result).rejects.toThrow('denied');
  } finally {
    await callback.close();
  }
});
