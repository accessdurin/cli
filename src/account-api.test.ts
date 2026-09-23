import { expect, it } from 'vitest';
import { DurinAccountApi } from './account-api.js';

it('refreshes an expired account once and retries with the rotated bearer without changing the mutation', async () => {
  let token = 'old',
    refreshes = 0;
  const calls: { bearer: string | null; body: unknown }[] = [];
  const api = new DurinAccountApi({
    appOrigin: 'https://app.example.test',
    accessToken: () => token,
    refresh: async () => {
      refreshes++;
      token = 'rotated';
    },
    fetch: async (_url, init) => {
      calls.push({ bearer: new Headers(init?.headers).get('authorization'), body: init?.body });
      return calls.length === 1
        ? Response.json({ error: { code: 'unauthorized', message: 'Expired' } }, { status: 401 })
        : Response.json({ ok: true });
    },
  });
  await api.start();
  expect(refreshes).toBe(1);
  expect(calls).toEqual([
    { bearer: 'Bearer old', body: '{}' },
    { bearer: 'Bearer rotated', body: '{}' },
  ]);
});
