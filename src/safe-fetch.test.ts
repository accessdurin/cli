import { expect, it } from 'vitest';
import { secureFetch } from './safe-fetch.js';

it.each(['http://issuer.example.test/token', 'https://user:secret@example.test/token'])(
  'rejects unsafe OAuth destination %s before sending credentials',
  async (url) => {
    const request = secureFetch(false, async () => {
      throw new Error('Must not send');
    });
    await expect(request(url, { method: 'POST', body: 'secret' })).rejects.toThrow('Unsafe');
  },
);
it('disallows redirect forwarding and preserves the caller abort signal', async () => {
  const controller = new AbortController();
  const request = secureFetch(false, async (_url, init) => {
    expect(init?.redirect).toBe('error');
    expect(init?.signal).toBeDefined();
    return Response.json({ ok: true });
  });
  expect(
    (await request('https://issuer.example.test/token', { signal: controller.signal })).status,
  ).toBe(200);
});
