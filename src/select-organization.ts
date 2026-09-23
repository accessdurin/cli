import { CliError } from './cli-options.js';
import type { OrganizationSelection } from './ports/cli-organization.js';
import type { AccountSession } from './ports/cli-auth.js';

const organizationId = async (options: OrganizationSelection): Promise<string> => {
  if (options.identity.organizations.length === 0)
    return (await options.create(await options.questions.create())).workosOrganizationId;
  const selected = await options.questions.choose(options.identity.organizations);
  if (!options.identity.organizations.some((organization) => organization.id === selected))
    throw new CliError('Choose an organization from your verified memberships.');
  return selected;
};

export async function selectOrganization(options: OrganizationSelection): Promise<AccountSession> {
  if (options.identity.principalId !== options.session.principalId)
    throw new CliError('Account identity changed. Run durin to sign in again.');
  const selected = await organizationId(options);
  const session = await options.refresh(options.session, selected);
  if (session.principalId !== options.identity.principalId || session.organizationId !== selected)
    throw new CliError('Account does not match the selected organization.');
  return session;
}
