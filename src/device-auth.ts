import { CliError } from './cli-options.js';
import { safeCliUrl } from './contracts/cli-url.js';
import type {
  AccountSession,
  AuthorizationPrompt,
  DeviceAccountAuth,
  PollingClock,
} from './ports/cli-auth.js';
import {
  deviceChallengeSchema,
  deviceErrorSchema,
  deviceSessionSchema,
} from './contracts/cli-auth.js';

type Options = {
  readonly clientId: string;
  readonly origin: string;
  readonly fetch: typeof fetch;
  readonly clock: PollingClock;
  readonly development?: boolean;
};
type Challenge = ReturnType<typeof deviceChallengeSchema.parse>;
const POLL_DELAYS: Readonly<Record<string, number>> = { authorization_pending: 0, slow_down: 5000 };
const nextInterval = (body: unknown, interval: number): number => {
  const increment = POLL_DELAYS[deviceErrorSchema.parse(body).error];
  if (increment === undefined)
    throw new CliError('Account authorization failed. Run durin to sign in again.');
  return interval + increment;
};
const browserUrl = (challenge: Challenge, development = false): string => {
  const url = new URL(challenge.verification_uri_complete ?? challenge.verification_uri);
  const base = new URL(url);
  base.search = '';
  if (!safeCliUrl(base.href, development)) throw new CliError('Unsafe account authorization URL.');
  return url.href;
};
const boundSession = (
  next: AccountSession,
  previous: AccountSession,
  organizationId: string | null,
): AccountSession => {
  if (next.principalId !== previous.principalId || next.organizationId !== organizationId)
    throw new CliError('Refreshed account does not match the selected profile.');
  return next;
};

export class WorkosDeviceAuth implements DeviceAccountAuth {
  constructor(private readonly options: Options) {}

  async refresh(
    session: AccountSession,
    organizationId = session.organizationId,
  ): Promise<AccountSession> {
    const response = await this.post('/user_management/authenticate', {
      grant_type: 'refresh_token',
      refresh_token: session.refreshToken,
      ...(organizationId ? { organization_id: organizationId } : {}),
    });
    if (!response.ok)
      throw new CliError('Account session could not be refreshed. Run durin to sign in again.');
    return boundSession(deviceSessionSchema.parse(await response.json()), session, organizationId);
  }

  async authorize(
    present: (prompt: AuthorizationPrompt) => Promise<void>,
  ): Promise<AccountSession> {
    const response = await this.post('/user_management/authorize/device', {});
    if (!response.ok) throw new CliError('Unable to start account sign-in.');
    const challenge = deviceChallengeSchema.parse(await response.json());
    const deadline = this.options.clock.now() + challenge.expires_in * 1000;
    await present({
      code: challenge.user_code,
      url: browserUrl(challenge, this.options.development),
    });
    return this.poll(challenge, deadline);
  }

  private async poll(
    challenge: Challenge,
    deadline: number,
    interval = challenge.interval * 1000,
  ): Promise<AccountSession> {
    this.assertFresh(deadline);
    await this.options.clock.sleep(Math.min(interval, deadline - this.options.clock.now()));
    this.assertFresh(deadline);
    const response = await this.post(
      '/user_management/authenticate',
      {
        grant_type: 'urn:ietf:params:oauth:grant-type:device_code',
        device_code: challenge.device_code,
      },
      deadline - this.options.clock.now(),
    );
    const body: unknown = await response.json();
    this.assertFresh(deadline);
    if (response.ok) return deviceSessionSchema.parse(body);
    return this.poll(challenge, deadline, nextInterval(body, interval));
  }

  private assertFresh(deadline: number): void {
    if (this.options.clock.now() >= deadline)
      throw new CliError('Account authorization expired. Run durin to sign in again.');
  }

  private post(path: string, fields: Record<string, string>, timeout = 15_000): Promise<Response> {
    return this.options.fetch(new URL(path, this.options.origin), {
      method: 'POST',
      redirect: 'error',
      signal: AbortSignal.timeout(Math.min(timeout, 15_000)),
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ client_id: this.options.clientId, ...fields }),
    });
  }
}
