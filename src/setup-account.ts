import { createHash } from 'node:crypto';
import type { AccountSession, DeviceAccountAuth, PollingClock } from './ports/cli-auth.js';
import type { AccountIntent, SetupUi } from './ports/cli-setup.js';
import { cliAuthConfigSchema } from './contracts/cli-auth.js';
import { safeCliUrl } from './contracts/cli-url.js';
import { WorkosDeviceAuth } from './device-auth.js';
import { DurinAccountApi } from './account-api.js';
import { selectOrganization } from './select-organization.js';
import { CliError } from './cli-options.js';

type Options = {
  readonly appOrigin: string;
  readonly development: boolean;
  readonly request: typeof fetch;
  readonly clock: PollingClock;
  readonly ui: SetupUi;
  readonly intent: AccountIntent;
};
const creationKey = (session: AccountSession, appOrigin: string, name: string): string => {
  const hex = createHash('sha256')
    .update(JSON.stringify([appOrigin, session.principalId, name]))
    .digest('hex');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-5${hex.slice(13, 16)}-a${hex.slice(17, 20)}-${hex.slice(20, 32)}`;
};
async function nativeConfiguration(options: Options) {
  const response = await options.request(new URL('/auth/cli/config', options.appOrigin));
  if (!response.ok)
    throw new CliError(
      'Native account authentication is not configured on this Durin deployment. Ask its administrator to enable AuthKit CLI authentication.',
    );
  const config = cliAuthConfigSchema.parse(await response.json());
  const trusted = [
    config.appOrigin === options.appOrigin,
    safeCliUrl(config.issuer, options.development),
    safeCliUrl(config.providerOrigin, options.development),
  ].every(Boolean);
  if (!trusted)
    throw new CliError('Native authentication configuration does not match this deployment.');
  return config;
}

export async function setupAccount(options: Options) {
  const config = await nativeConfiguration(options);
  const auth = new WorkosDeviceAuth({
    clientId: config.clientId,
    origin: config.providerOrigin,
    fetch: options.request,
    clock: options.clock,
    development: options.development,
  });
  const session = await auth.authorize((prompt) =>
    options.ui.account.authorize(prompt, options.intent),
  );
  const scope = {
    appOrigin: options.appOrigin,
    accountIssuer: config.issuer,
    accountClientId: config.clientId,
    principalId: session.principalId,
  };
  const api = (value: AccountSession) =>
    new DurinAccountApi({
      appOrigin: options.appOrigin,
      accessToken: value.accessToken,
      fetch: options.request,
    });
  const identity = await api(session).session();
  const selected = await selectOrganization({
    session,
    identity,
    questions: options.ui.organizations,
    refresh: (previous, organizationId) => auth.refresh(previous, organizationId),
    create: (name) =>
      api(session).createOrganization({
        name,
        idempotencyKey: creationKey(session, options.appOrigin, name),
      }),
  });
  return { ...(await selectedAccount(options, auth, selected)), scope };
}

async function selectedAccount(options: Options, auth: DeviceAccountAuth, session: AccountSession) {
  let selected = session;
  const selectedApi = new DurinAccountApi({
    appOrigin: options.appOrigin,
    fetch: options.request,
    accessToken: () => selected.accessToken,
    refresh: async () => {
      selected = await auth.refresh(selected);
    },
  });
  const verified = await selectedApi.session();
  if (
    verified.organizationId !== selected.organizationId ||
    verified.principalId !== selected.principalId
  )
    throw new CliError('The verified account does not match the selected organization.');
  return { api: selectedApi, identity: verified };
}
