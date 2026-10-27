import type { CliProfile } from './contracts/cli-profile.js';
import type { CredentialStore } from './ports/credential-store.js';
import type { PollingClock } from './ports/cli-auth.js';
import type { AccountIntent, SetupUi } from './ports/cli-setup.js';
import { NativeCredentialStore } from './credentials.js';
import { FileProfiles, createProfile } from './profiles.js';
import { agentInstalled } from './agent-installed.js';
import { localAgents, type AgentConfig } from './agents.js';
import { writeBridgeConfig } from './bridge-config.js';
import { setupAccount } from './setup-account.js';
import { onboardOrganization } from './onboard-organization.js';
import { authorizeMcp, mcpIssuer } from './setup-mcp.js';
import { pollingClock } from './polling-clock.js';
import { secureFetch } from './safe-fetch.js';
import { withProfileLock } from './profile-lock.js';
import { FilePendingRevocations, FileRevocationMarker } from './revocation-marker.js';
import { retryPendingRevocations } from './logout.js';
import { installRuntime } from './install-runtime.js';
import { verifySavedBridge } from './verify-bridge.js';
import { CliError } from './cli-options.js';
import { deploymentHostAllowlist } from './contracts/cli-host.js';

type Options = {
  readonly appOrigin: string;
  readonly development: boolean;
  readonly directory: string;
  readonly programPath: string;
  readonly ui: SetupUi;
  readonly intent?: AccountIntent;
  readonly request?: typeof fetch;
  readonly store?: CredentialStore;
  readonly clock?: PollingClock;
  readonly agents?: Readonly<Record<string, AgentConfig>>;
};
export async function runSetup(options: Options): Promise<void> {
  const context = await setupContext(options);
  const { intent, request, clock } = context;
  options.ui.stage('Authenticate your Durin account');
  const account = await setupAccount({ ...options, request, intent, clock });
  options.ui.stage('Complete organization onboarding');
  await onboardOrganization({
    api: account.api,
    questions: onboardingQuestions(options, account.identity.organizationId),
    clock,
  });
  await setupAgent(context, account);
}
const onboardingQuestions = (options: Options, workosOrganizationId: string) => ({
  answer: options.ui.onboarding.answer,
  payment: async (organizationId: string) => {
    await options.ui.onboarding.payment(organizationId);
    const url = new URL('/auth/login', options.appOrigin);
    url.search = new URLSearchParams({
      provider: 'authkit',
      organizationId: workosOrganizationId,
    }).toString();
    await options.ui.openAuthorization(url.href);
  },
});
async function setupContext(options: Options) {
  const intent = options.intent ?? (await options.ui.account.intent());
  return {
    ...options,
    intent,
    request: secureFetch(deploymentHostAllowlist(options), options.request ?? fetch),
    store: options.store ?? new NativeCredentialStore(),
    clock: setupClock(options),
  };
}
async function setupAgent(
  options: Awaited<ReturnType<typeof setupContext>>,
  account: Awaited<ReturnType<typeof setupAccount>>,
): Promise<void> {
  const { request } = options;
  const workspace = await account.api.workspace();
  assertWorkspace(account.identity, workspace);
  const agent = await chosenAgent(options);
  const endpoint =
    workspace.mcp.status === 'needs_setup' ? await account.api.setupEndpoint() : workspace.mcp;
  if (endpoint.status !== 'ready')
    throw new CliError(
      'Personal MCP endpoint setup is unavailable. Ask your administrator to enable MCP.',
    );
  const profile = createProfile({
    ...account.scope,
    workosOrganizationId: account.identity.organizationId,
    organizationId: workspace.organization.id,
    environment: workspace.settings.environment,
    resource: endpoint.url,
    mcpIssuer: await mcpIssuer(endpoint.url, request, options),
    development: options.development,
  });
  const profiles = new FileProfiles(options.directory);
  await profiles.save(profile);
  const bridge = {
    nodePath: process.execPath,
    cliPath: await installRuntime(options.directory, options.programPath),
    configDirectory: options.directory,
    profileId: profile.id,
  };
  await writeBridgeConfig(agent, bridge);
  options.ui.agent.configured(agent.configPath);
  options.ui.stage('Sign in to this organization’s Durin MCP');
  await signInMcp({ ...options, profile, open: options.ui.openAuthorization });
  options.ui.stage('Verify the saved agent bridge');
  const toolCount = await verifySavedBridge(agent, bridge);
  await profiles.activate(profile.id);
  options.ui.agent.ready({
    organization: workspace.organization.name,
    agent: agent.name,
    profileId: profile.id,
    toolCount,
  });
}
/** Signs the profile in to its MCP server, first retrying revocations an earlier sign-in left. */
export async function signInMcp(input: {
  readonly directory: string;
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly request: typeof fetch;
  readonly open: (url: string) => Promise<void>;
}): Promise<number> {
  const { directory, profile, store, request } = input;
  await retryPendingRevocations(directory, profile, store, request);
  return withProfileLock(directory, profile.id, () =>
    authorizeMcp({
      profile,
      store,
      marker: new FileRevocationMarker(directory, profile.id),
      pending: new FilePendingRevocations(directory, profile.id, store),
      request,
      open: input.open,
    }),
  );
}
async function chosenAgent(options: Options): Promise<AgentConfig> {
  const key = await options.ui.agent.choose();
  const agents = setupAgents(options);
  const agent = Object.hasOwn(agents, key) ? agents[key] : undefined;
  if (!agent) throw new CliError('Choose a supported agent during onboarding.');
  if (!agentInstalled(key))
    throw new CliError(
      `Install ${agent.name}, then run durin to resume. Your organization answers are saved.`,
    );
  return agent;
}
const setupAgents = (options: Options) => options.agents ?? localAgents();
function assertWorkspace(
  identity: { principalId: string; organizationId: string },
  workspace: {
    session: { principalId: string; organizationId: string };
    membership: { principalId: string; active: boolean };
  },
): void {
  const matches = [
    identity.principalId === workspace.session.principalId,
    identity.organizationId === workspace.session.organizationId,
    workspace.membership.active,
    workspace.membership.principalId === identity.principalId,
  ].every(Boolean);
  if (!matches) throw new CliError('Workspace does not match your verified membership.');
}

const setupClock = (options: Options): PollingClock => options.clock ?? pollingClock();
