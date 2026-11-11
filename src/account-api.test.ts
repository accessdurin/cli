import { expect, it } from 'vitest';
import { DurinAccountApi } from './account-api.js';

it.each([200, 401])(
  'refreshes an expired account once without changing the mutation, even when the retry returns %s',
  async (status) => {
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
      fetch: async (_url, init = {}) => {
        calls.push({ bearer: new Headers(init.headers).get('authorization'), body: init.body });
        const responseStatus = calls.length === 1 ? 401 : status;
        const body =
          responseStatus === 200
            ? { ok: true }
            : { error: { code: 'unauthorized', message: 'Expired' } };
        return Response.json(body, { status: responseStatus });
      },
    });
    const error = await api.start().then(
      () => null,
      (failure: Error) => failure.message,
    );
    expect(error).toBe(status === 200 ? null : 'Durin request failed: unauthorized (401).');
    expect(refreshes).toBe(1);
    expect(calls).toEqual([
      { bearer: 'Bearer old', body: '{}' },
      { bearer: 'Bearer rotated', body: '{}' },
    ]);
  },
);
