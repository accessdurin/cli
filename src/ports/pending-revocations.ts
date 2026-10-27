/** Only what revoking a grant needs: the public client that holds it and its tokens. */
export type UnrevokedGrant = {
  readonly clientId: string;
  readonly accessToken: string;
  readonly refreshToken?: string | undefined;
};
/**
 * Grants that a new sign-in replaced before the authorization server confirmed their revocation.
 * Their tokens are kept in the OS credential store, never on disk.
 */
export interface PendingRevocations {
  entries(): Promise<readonly UnrevokedGrant[]>;
  add(grant: UnrevokedGrant): Promise<void>;
  replace(grants: readonly UnrevokedGrant[]): Promise<void>;
}
