import type { AuthorizationPrompt } from './cli-auth.js';
import type { OrganizationSelectionQuestions } from './cli-organization.js';
import type { OrganizationQuestions } from './cli-onboarding.js';

export type AccountIntent = 'login' | 'signup';
export interface SetupAccountQuestions {
  intent(): Promise<AccountIntent>;
  authorize(prompt: AuthorizationPrompt, intent: AccountIntent): Promise<void>;
}
export interface SetupAgentQuestions {
  choose(): Promise<string>;
  configured(path: string): void;
  ready(result: {
    organization: string;
    agent: string;
    profileId: string;
    toolCount: number;
  }): void;
}
export interface SetupUi {
  readonly account: SetupAccountQuestions;
  readonly organizations: OrganizationSelectionQuestions;
  readonly onboarding: OrganizationQuestions;
  readonly agent: SetupAgentQuestions;
  readonly openAuthorization: (url: string) => Promise<void>;
  readonly stage: (message: string) => void;
}
