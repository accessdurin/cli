import { expect, it } from 'vitest';
import { secureFetch } from './safe-fetch.js';

const allowlist = { hosts: ['issuer.example.test'], development: false };
const mustNotSend: typeof fetch = async () => {
  throw new Error('Must not send');
};

it.each(['http://issuer.example.test/token', 'https://user:secret@issuer.example.test/token'])(
  'rejects unsafe OAuth destination %s before sending credentials',
  async (url) => {
    const request = secureFetch(allowlist, mustNotSend);
    await expect(request(url, { method: 'POST', body: 'secret' })).rejects.toThrow('Unsafe');
  },
);
it.each([
  'https://evil.example/token',
  'https://issuer.example.test.evil.example/token',
  'https://evil-issuer.example.test/token',
  'https://attacker.issuer.example.test/token',
  'https://example.test/token',
  'https://issuer.example.test:8443/token',
  'https://localhost/token',
  'https://127.0.0.1/token',
])('refuses %s, a host not on the allowlist, before sending anything', async (url) => {
  const request = secureFetch(allowlist, mustNotSend);
  await expect(request(url, { method: 'POST', body: 'secret' })).rejects.toThrow(
    `durin didn’t contact ${new URL(url).host}`,
  );
  await expect(request(new Request(url, { method: 'POST', body: 'secret' }))).rejects.toThrow(
    'didn’t contact',
  );
});
it('explains a refused host in plain language', async () => {
  const request = secureFetch(allowlist, mustNotSend);
  const problem = String(await request('https://evil.example/revoke').catch((error) => error));
  expect(problem).toContain('evil.example');
  expect(problem).toContain('issuer.example.test');
  expect(problem).toContain('Nothing was sent');
  expect(problem).not.toMatch(/Durin|https?:\/\//u);
});
it('reaches a listed host whatever its case', async () => {
  const request = secureFetch(allowlist, async () => Response.json({ ok: true }));
  expect((await request('https://ISSUER.Example.test/token')).status).toBe(200);
  expect((await request('https://issuer.example.test:443/token')).status).toBe(200);
});
it('reaches loopback hosts only in development', async () => {
  const ok = async () => Response.json({ ok: true });
  const listedLoopback = secureFetch({ hosts: ['127.0.0.1'], development: false }, ok);
  await expect(listedLoopback('https://127.0.0.1/token')).rejects.toThrow('--dev');
  const development = secureFetch({ hosts: [], development: true }, ok);
  expect((await development('http://127.0.0.1:5272/token')).status).toBe(200);
  await expect(development('https://evil.example/token')).rejects.toThrow('didn’t contact');
});
it('disallows redirect forwarding and preserves the caller abort signal', async () => {
  const controller = new AbortController();
  const request = secureFetch(allowlist, async (_url, init) => {
    expect(init?.redirect).toBe('error');
    expect(init?.signal).toBeDefined();
    return Response.json({ ok: true });
  });
  expect(
    (await request('https://issuer.example.test/token', { signal: controller.signal })).status,
  ).toBe(200);
});
