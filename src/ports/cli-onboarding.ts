import type { OnboardingAnswer, OnboardingStatus } from '../contracts/onboarding.js';

export interface OrganizationOnboardingApi {
  status(): Promise<OnboardingStatus>;
  start(): Promise<void>;
  answer(answer: OnboardingAnswer): Promise<OnboardingStatus>;
  complete(): Promise<OnboardingStatus>;
}

export interface OrganizationQuestions {
  answer(step: OnboardingAnswer['step'], version: number): Promise<OnboardingAnswer>;
  payment(organizationId: string): Promise<void>;
}
