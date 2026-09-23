import type { AccountSession } from './cli-auth.js';

export interface VerifiedOrganizations {
  readonly principalId: string;
  readonly organizationId: string;
  readonly organizations: readonly { readonly id: string; readonly name: string }[];
}
export interface OrganizationSelectionQuestions {
  choose(organizations: VerifiedOrganizations['organizations']): Promise<string>;
  create(): Promise<string>;
}
export interface OrganizationSelection {
  readonly session: AccountSession;
  readonly identity: VerifiedOrganizations;
  readonly questions: OrganizationSelectionQuestions;
  readonly refresh: (session: AccountSession, organizationId: string) => Promise<AccountSession>;
  readonly create: (name: string) => Promise<{ readonly workosOrganizationId: string }>;
}
