import * as Sentry from '@sentry/node';
import { nodeProfilingIntegration } from '@sentry/profiling-node';
import { version } from '../package.json';
import { CliError } from './cli-options.js';
import { makeTelemetryTransport, scrubEvent, scrubSpan } from './telemetry-privacy.js';

const SENTRY_DSN =
  'https://19cf98c3143c037e2d0306ffcc283bb5@o4512105381494784.ingest.us.sentry.io/4512354708422656';
const FLUSH_TIMEOUT_MS = 2_000;

const argumentError = (error: TypeError): boolean =>
  'code' in error && typeof error.code === 'string' && error.code.startsWith('ERR_PARSE_ARGS_');

const expectedError = (error: unknown): boolean =>
  error instanceof CliError || (error instanceof TypeError && argumentError(error));

const trackingDisabled = (): boolean =>
  ['1', 'true'].includes(process.env.DO_NOT_TRACK?.toLowerCase() ?? '');

export function initializeTelemetry(): void {
  try {
    Sentry.init({
      dsn: SENTRY_DSN,
      enabled: process.env.NODE_ENV !== 'test' && !trackingDisabled(),
      release: `@accessdurin/cli@${version}`,
      environment: 'production',
      debug: false,
      spotlight: false,
      defaultIntegrations: false,
      integrations: [
        nodeProfilingIntegration(),
        Sentry.dedupeIntegration(),
        Sentry.onUncaughtExceptionIntegration(),
        Sentry.onUnhandledRejectionIntegration({ mode: 'strict' }),
      ],
      dataCollection: {
        userInfo: false,
        cookies: false,
        httpHeaders: false,
        httpBodies: [],
        urlQueryParams: false,
        stackFrameVariables: false,
        frameContextLines: 0,
      },
      tracesSampleRate: 1,
      profileSessionSampleRate: 1,
      profileLifecycle: 'trace',
      enableRuntimeChannelInjection: false,
      sendClientReports: false,
      maxBreadcrumbs: 0,
      shutdownTimeout: FLUSH_TIMEOUT_MS,
      transportOptions: { keepAlive: false },
      transport: makeTelemetryTransport,
      beforeSend: scrubEvent,
      beforeSendSpan: Sentry.withStreamedSpan(scrubSpan),
      beforeSendLog: () => null,
      beforeSendMetric: () => null,
    });
  } catch {
    // Reporting must never prevent a CLI command from running.
  }
}

export function reportUnexpectedError(error: unknown): void {
  if (expectedError(error)) return;
  try {
    Sentry.captureException(
      error instanceof Error ? error : new Error('Durin CLI operation failed'),
    );
  } catch {
    // Preserve the original error and exit code if reporting is unavailable.
  }
}

export async function flushTelemetry(): Promise<void> {
  try {
    await Sentry.flush(FLUSH_TIMEOUT_MS);
  } catch {
    // Preserve command output and exit status when delivery fails.
  }
}

export async function traceCommand<T>(action: () => Promise<T>): Promise<T> {
  let started = false;
  try {
    return await Sentry.startSpan({ name: 'durin command', op: 'cli.command' }, () => {
      started = true;
      return action();
    });
  } catch (error) {
    if (started) throw error;
    return action();
  }
}
