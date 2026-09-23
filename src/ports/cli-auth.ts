export interface AccountSession {
  readonly accessToken: string;
  readonly refreshToken: string;
  readonly principalId: string;
  readonly organizationId: string | null;
}

export interface AuthorizationPrompt {
  readonly code: string;
  readonly url: string;
}

export interface DeviceAccountAuth {
  authorize(present: (prompt: AuthorizationPrompt) => Promise<void>): Promise<AccountSession>;
  refresh(session: AccountSession, organizationId?: string | null): Promise<AccountSession>;
}

export interface PollingClock {
  now(): number;
  sleep(milliseconds: number): Promise<void>;
}
