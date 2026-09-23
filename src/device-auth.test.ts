import { describe, expect, it } from 'vitest';
import { WorkosDeviceAuth } from './device-auth.js';

const challenge = {
  device_code: 'private-device-code',
  user_code: 'ABCD-EFGH',
  verification_uri: 'https://auth.example/device',
  verification_uri_complete: 'https://auth.example/device?user_code=ABCD-EFGH',
  expires_in: 300,
  interval: 5,
};
const tokens = {
  access_token: 'private-access-token',
  refresh_token: 'private-refresh-token',
  organization_id: 'org_acme',
  user: { id: 'user_admin' },
};

function fixture(replies: unknown[], expires = 300) {
  let now = 0;
  const delays: number[] = [];
  const queue = [{ ...challenge, expires_in: expires }, ...replies];
  const auth = new WorkosDeviceAuth({
    clientId: 'client_public',
    origin: 'https://api.workos.com',
    fetch: async () => {
      const body = queue.shift();
      return Response.json(body, { status: body === tokens || now === 0 ? 200 : 400 });
    },
    clock: {
      now: () => now,
      sleep: async (ms) => {
        delays.push(ms);
        now += ms;
      },
    },
  });
  return { auth, delays, queue };
}

describe('WorkOS device authorization', () => {
  it('switches organizations using a rotating refresh grant with no client secret', async () => {
    let request: RequestInit | undefined;
    const auth = new WorkosDeviceAuth({
      clientId: 'client_public',
      origin: 'https://api.workos.com',
      fetch: async (_url, init) => {
        request = init;
        return Response.json({ ...tokens, organization_id: 'org_selected' });
      },
      clock: { now: () => 0, sleep: async () => {} },
    });
    const session = {
      accessToken: 'old-access',
      refreshToken: 'old-refresh',
      principalId: 'user_admin',
      organizationId: 'org_acme',
    };
    await expect(auth.refresh(session, 'org_selected')).resolves.toEqual({
      accessToken: tokens.access_token,
      refreshToken: tokens.refresh_token,
      principalId: 'user_admin',
      organizationId: 'org_selected',
    });
    expect(Object.fromEntries(new URLSearchParams(String(request?.body)))).toEqual({
      client_id: 'client_public',
      grant_type: 'refresh_token',
      refresh_token: 'old-refresh',
      organization_id: 'org_selected',
    });
    expect(request?.redirect).toBe('error');
  });
  it('waits for browser approval and returns the authenticated session', async () => {
    const requests: URLSearchParams[] = [];
    const replies = [challenge, { error: 'authorization_pending' }, tokens];
    const delays: number[] = [];
    let now = 0;
    const auth = new WorkosDeviceAuth({
      clientId: 'client_public',
      origin: 'https://api.workos.com',
      fetch: async (_url, init) => {
        requests.push(new URLSearchParams(String(init?.body)));
        const body = replies.shift();
        return Response.json(body, { status: body === tokens || body === challenge ? 200 : 400 });
      },
      clock: {
        now: () => now,
        sleep: async (ms) => {
          delays.push(ms);
          now += ms;
        },
      },
    });
    const presented: unknown[] = [];
    const result = await auth.authorize(async (value) => {
      presented.push(value);
    });
    expect(result).toEqual({
      accessToken: tokens.access_token,
      refreshToken: tokens.refresh_token,
      organizationId: 'org_acme',
      principalId: 'user_admin',
    });
    expect(presented).toEqual([{ code: 'ABCD-EFGH', url: challenge.verification_uri_complete }]);
    expect(delays).toEqual([5000, 5000]);
    expect(requests.map((body) => Object.fromEntries(body))).toEqual([
      { client_id: 'client_public' },
      {
        client_id: 'client_public',
        device_code: challenge.device_code,
        grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
      },
      {
        client_id: 'client_public',
        device_code: challenge.device_code,
        grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
      },
    ]);
  });

  it('respects slow_down for every subsequent poll', async () => {
    const run = fixture([{ error: 'slow_down' }, { error: 'authorization_pending' }, tokens]);
    await expect(run.auth.authorize(async () => {})).resolves.toMatchObject({
      principalId: 'user_admin',
    });
    expect(run.delays).toEqual([5000, 10000, 10000]);
  });

  it('does not accept a token after the device authorization expires', async () => {
    const run = fixture([{ error: 'authorization_pending' }, tokens], 10);
    await expect(run.auth.authorize(async () => {})).rejects.toThrow('expired');
    expect(run.queue).toEqual([tokens]);
  });

  it.each(['access_denied', 'expired_token', 'invalid_client'])(
    'stops on %s without displaying provider secrets',
    async (error) => {
      const run = fixture([{ error, error_description: 'private-provider-detail' }, tokens]);
      await expect(run.auth.authorize(async () => {})).rejects.toThrow(
        'Account authorization failed.',
      );
      expect(run.queue).toEqual([tokens]);
    },
  );

  it.each(['javascript:alert(1)', 'http://auth.example/device'])(
    'rejects an unsafe browser URL: %s',
    async (url) => {
      const auth = new WorkosDeviceAuth({
        clientId: 'client_public',
        origin: 'https://api.workos.com',
        fetch: async () => Response.json({ ...challenge, verification_uri_complete: url }),
        clock: { now: () => 0, sleep: async () => {} },
      });
      const presented: unknown[] = [];
      await expect(
        auth.authorize(async (value) => {
          presented.push(value);
        }),
      ).rejects.toThrow('authorization URL');
      expect(presented).toEqual([]);
    },
  );
});
