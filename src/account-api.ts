import { CliError } from './cli-options.js';
import { z } from 'zod';
import {
  onboardingAnswerSchema,
  onboardingStatusSchema,
  type OnboardingAnswer,
} from './contracts/onboarding.js';
import { organizationCreateSchema } from './contracts/account.js';
import { personalBootstrapSchema } from './contracts/personal-bootstrap.js';
import { mcpEndpointSchema } from './contracts/mcp-endpoint.js';
import { apiErrorSchema, sessionSchema, organizationCreatedSchema } from './contracts/account.js';
import type { OrganizationOnboardingApi } from './ports/cli-onboarding.js';

type Options = {
  readonly appOrigin: string;
  readonly accessToken: string | (() => string);
  readonly fetch: typeof fetch;
  readonly refresh?: () => Promise<void>;
};
export class AccountApiError extends CliError {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`Durin request failed: ${code} (${status}).`);
  }
}
const responseError = (status: number, body: unknown): AccountApiError => {
  const parsed = apiErrorSchema.safeParse(body);
  const code = parsed.success ? parsed.data.error.code : 'invalid_response';
  return new AccountApiError(status, /^[a-z_]{1,100}$/iu.test(code) ? code : 'invalid_response');
};

export class DurinAccountApi implements OrganizationOnboardingApi {
  constructor(private readonly options: Options) {}

  session() {
    return this.request('GET', '/v1/session', sessionSchema);
  }
  workspace() {
    return this.request('GET', '/v1/me/bootstrap', personalBootstrapSchema);
  }
  setupEndpoint() {
    return this.request('POST', '/v1/mcp-endpoint', mcpEndpointSchema, {});
  }
  createOrganization(input: z.infer<typeof organizationCreateSchema>) {
    return this.request(
      'POST',
      '/v1/organizations',
      organizationCreatedSchema,
      organizationCreateSchema.parse(input),
    );
  }

  status() {
    return this.request('GET', '/v1/onboarding', onboardingStatusSchema);
  }

  async start(): Promise<void> {
    await this.request('POST', '/v1/onboarding/start', z.object({ ok: z.literal(true) }), {});
  }

  answer(answer: OnboardingAnswer) {
    return this.request(
      'PUT',
      '/v1/onboarding/answer',
      onboardingStatusSchema,
      onboardingAnswerSchema.parse(answer),
    );
  }

  complete() {
    return this.request('POST', '/v1/onboarding/complete', onboardingStatusSchema, {});
  }

  private async request<T>(
    method: string,
    path: string,
    schema: z.ZodType<T>,
    body?: unknown,
  ): Promise<T> {
    const response = await this.send(method, path, body);
    const result: unknown = await response.json();
    if (!response.ok) throw responseError(response.status, result);
    return schema.parse(result);
  }

  private token(): string {
    return typeof this.options.accessToken === 'string'
      ? this.options.accessToken
      : this.options.accessToken();
  }
  private async send(method: string, path: string, body: unknown, retry = true): Promise<Response> {
    const response = await this.options.fetch(new URL(`/api${path}`, this.options.appOrigin), {
      method,
      redirect: 'error',
      signal: AbortSignal.timeout(15_000),
      headers: {
        authorization: `Bearer ${this.token()}`,
        origin: this.options.appOrigin,
        'x-requested-with': 'durin',
        'content-type': 'application/json',
      },
      ...requestBody(body),
    });
    const refresh = refreshHandler(response.status, retry, this.options.refresh);
    if (refresh) {
      await response.body?.cancel();
      await refresh();
      return this.send(method, path, body, false);
    }
    return response;
  }
}

const requestBody = (body: unknown) => (body === undefined ? {} : { body: JSON.stringify(body) });
const refreshHandler = (status: number, retry: boolean, refresh: Options['refresh']) =>
  status === 401 && retry ? refresh : undefined;
