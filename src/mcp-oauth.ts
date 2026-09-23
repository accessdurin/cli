import { CliError } from './cli-options.js';
import { z } from 'zod';
import { OAuthClientInformationSchema, OAuthTokensSchema } from '@modelcontextprotocol/core';
import type {
  OAuthClientInformationContext,
  OAuthClientProvider,
  OAuthDiscoveryState,
  OAuthClientMetadata,
  StoredOAuthClientInformation,
  StoredOAuthTokens,
} from '@modelcontextprotocol/client';
import type { CredentialStore } from './ports/credential-store.js';
import type { CliProfile } from './contracts/cli-profile.js';

type Authorization = {
  readonly redirectUrl: string;
  readonly state: string;
  readonly open: (url: URL) => Promise<void>;
};
type Options = {
  readonly profile: CliProfile;
  readonly store: CredentialStore;
  readonly authorization?: Authorization;
};
type CredentialKind = 'client' | 'tokens';
const signInRequired = () =>
  new CliError('MCP sign-in is required. Run durin in a terminal to authorize this organization.');
const bindingSchema = z.object({ issuer: z.string() });
const clientMetadataSchema = z.object({
  redirect_uris: z.array(z.url()).optional(),
  token_endpoint_auth_method: z.literal('none').optional(),
});
const registeredRedirects = (value: unknown): readonly string[] =>
  clientMetadataSchema.parse(value).redirect_uris ?? [];
function requireIssuer(value: unknown, issuer: string): string {
  if (bindingSchema.parse(value).issuer !== issuer)
    throw new CliError('MCP credential issuer mismatch.');
  return issuer;
}
const parseTokens = (value: unknown, issuer: string): StoredOAuthTokens => ({
  ...OAuthTokensSchema.parse(value),
  issuer: requireIssuer(value, issuer),
});
const parseClient = (value: unknown, issuer: string): StoredOAuthClientInformation => ({
  ...OAuthClientInformationSchema.parse(value),
  ...clientMetadataSchema.parse(value),
  issuer: requireIssuer(value, issuer),
});

export class McpOAuthProvider implements OAuthClientProvider {
  private verifier: string | undefined;
  private discovery: OAuthDiscoveryState | undefined;
  constructor(private readonly options: Options) {}

  get redirectUrl(): string {
    return this.options.authorization?.redirectUrl ?? 'http://127.0.0.1/oauth/callback';
  }
  get clientMetadata(): OAuthClientMetadata {
    return {
      client_name: 'Durin CLI',
      application_type: 'native',
      redirect_uris: [this.redirectUrl],
      grant_types: ['authorization_code', 'refresh_token'],
      response_types: ['code'],
      token_endpoint_auth_method: 'none',
    };
  }

  state(): string {
    return this.authorization().state;
  }
  async redirectToAuthorization(url: URL): Promise<void> {
    await this.authorization().open(url);
  }
  saveCodeVerifier(verifier: string): void {
    this.verifier = verifier;
  }
  codeVerifier(): string {
    if (!this.verifier) throw signInRequired();
    return this.verifier;
  }

  async clientInformation(
    context?: OAuthClientInformationContext,
  ): Promise<StoredOAuthClientInformation | undefined> {
    const client = await this.load('client', parseClient, context);
    if (!client && !this.options.authorization) throw signInRequired();
    return this.clientForAuthorization(client);
  }
  async saveClientInformation(
    value: StoredOAuthClientInformation,
    context?: OAuthClientInformationContext,
  ): Promise<void> {
    this.assertIssuer(context);
    await this.options.store.write(
      this.key('client'),
      JSON.stringify(parseClient(value, this.options.profile.mcpIssuer)),
    );
  }
  tokens(context?: OAuthClientInformationContext): Promise<StoredOAuthTokens | undefined> {
    return this.load('tokens', parseTokens, context);
  }
  async saveTokens(
    value: StoredOAuthTokens,
    context?: OAuthClientInformationContext,
  ): Promise<void> {
    this.assertIssuer(context);
    const tokens = parseTokens(value, this.options.profile.mcpIssuer);
    await this.options.store.write(this.key('tokens'), JSON.stringify(tokens));
  }

  async validateResourceURL(serverUrl: string | URL, resource?: string): Promise<URL> {
    if (
      ![
        String(serverUrl) === this.options.profile.resource,
        resource === this.options.profile.resource,
      ].every(Boolean)
    )
      throw new CliError('MCP resource does not match the selected organization profile.');
    return new URL(this.options.profile.resource);
  }

  saveDiscoveryState(state: OAuthDiscoveryState): void {
    if (state.authorizationServerUrl !== this.options.profile.mcpIssuer)
      throw new CliError('MCP authorization issuer does not match the selected profile.');
    this.discovery = state;
  }
  discoveryState(): OAuthDiscoveryState | undefined {
    return this.discovery;
  }

  async invalidateCredentials(
    scope: 'all' | 'client' | 'tokens' | 'verifier' | 'discovery',
  ): Promise<void> {
    const kinds: Readonly<Record<typeof scope, readonly CredentialKind[]>> = {
      all: ['client', 'tokens'],
      client: ['client'],
      tokens: ['tokens'],
      verifier: [],
      discovery: [],
    };
    await Promise.all(kinds[scope].map((kind) => this.options.store.remove(this.key(kind))));
    if (['all', 'verifier'].includes(scope)) this.verifier = undefined;
    if (['all', 'discovery'].includes(scope)) this.discovery = undefined;
  }

  private authorization(): Authorization {
    if (!this.options.authorization) throw signInRequired();
    return this.options.authorization;
  }
  private async clientForAuthorization(
    client: StoredOAuthClientInformation | undefined,
  ): Promise<StoredOAuthClientInformation | undefined> {
    if (!this.options.authorization) return client;
    if (!client) return undefined;
    const usable = [
      Boolean(await this.tokens()),
      registeredRedirects(client).includes(this.redirectUrl),
    ].some(Boolean);
    return usable ? client : undefined;
  }
  private key(kind: CredentialKind): string {
    return `mcp:${this.options.profile.id}:${kind}`;
  }
  private assertIssuer(context?: OAuthClientInformationContext): void {
    if (context && context.issuer !== this.options.profile.mcpIssuer)
      throw new CliError('MCP authorization issuer does not match the selected profile.');
  }
  private async load<T>(
    kind: CredentialKind,
    parse: (value: unknown, issuer: string) => T,
    context?: OAuthClientInformationContext,
  ): Promise<T | undefined> {
    this.assertIssuer(context);
    const raw = await this.options.store.read(this.key(kind));
    if (raw === null) return undefined;
    try {
      return parse(JSON.parse(raw), this.options.profile.mcpIssuer);
    } catch {
      throw new CliError(
        'Saved MCP credentials are invalid. Run durin to authorize this organization again.',
      );
    }
  }
}
