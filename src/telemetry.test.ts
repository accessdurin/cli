import * as Sentry from '@sentry/node';
import { performance } from 'node:perf_hooks';
import { createHash } from 'node:crypto';
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from 'vitest';
import { version } from '../package.json';
import { CliError } from './cli-options.js';
import {
  flushTelemetry,
  initializeTelemetry,
  reportUnexpectedError,
  traceCommand,
} from './telemetry.js';

const recorded = vi.hoisted(() => ({ envelopes: [] as unknown[] }));
vi.mock('@sentry/node', async (importOriginal) => {
  const sentry = await importOriginal<typeof Sentry>();
  return {
    ...sentry,
    init: vi.fn<typeof sentry.init>(sentry.init),
    makeNodeTransport: vi.fn<typeof sentry.makeNodeTransport>(() => ({
      send: (envelope) => {
        recorded.envelopes.push(envelope);
        return Promise.resolve({ statusCode: 200 });
      },
      flush: () => Promise.resolve(true),
    })),
    captureException: vi.fn<typeof sentry.captureException>(sentry.captureException),
    flush: vi.fn<typeof sentry.flush>(sentry.flush),
    startSpan: vi.fn<typeof sentry.startSpan>(sentry.startSpan),
  };
});

const exceptionListeners = process.listeners('uncaughtException');
const rejectionListeners = process.listeners('unhandledRejection');
beforeAll(() => {
  vi.stubEnv('SENTRY_DSN', 'https://override@invalid.example/1');
  vi.stubEnv('NODE_ENV', 'production');
  vi.stubEnv('DO_NOT_TRACK', undefined);
  initializeTelemetry();
});
beforeEach(() => {
  recorded.envelopes.length = 0;
  vi.clearAllMocks();
});
afterEach(flushTelemetry);
afterAll(async () => {
  await Sentry.close(2_000);
  for (const listener of process
    .listeners('uncaughtException')
    .filter((candidate) => !exceptionListeners.includes(candidate))) {
    process.removeListener('uncaughtException', listener);
  }
  for (const listener of process
    .listeners('unhandledRejection')
    .filter((candidate) => !rejectionListeners.includes(candidate))) {
    process.removeListener('unhandledRejection', listener);
  }
  vi.unstubAllEnvs();
});

it('uses the embedded DSN and sends scrubbed diagnostics through the real SDK before returning', async () => {
  const client = Sentry.getClient<Sentry.NodeClient>()!;
  expect(client.getDsn()).toMatchObject({
    publicKey: '19cf98c3143c037e2d0306ffcc283bb5',
    host: 'o4512105381494784.ingest.us.sentry.io',
    projectId: '4512354708422656',
  });
  const options = client.getOptions();
  expect(options.enabled).toBe(true);
  expect(options.integrations.map(({ name }) => name)).toEqual([
    'ProfilingIntegration',
    'Dedupe',
    'OnUncaughtException',
    'OnUnhandledRejection',
    'SpanStreaming',
  ]);
  expect(options.tracesSampleRate).toBe(1);
  expect(options.profileSessionSampleRate).toBe(1);
  expect(options.profileLifecycle).toBe('trace');
  expect(options.beforeSendLog!({ message: 'secret' } as Sentry.Log)).toBeNull();
  expect(options.beforeSendMetric!({ name: 'secret' } as Sentry.Metric)).toBeNull();

  const secret = 'private-token-and-tool-input';
  const error = new TypeError(secret, { cause: new Error(secret) });
  error.stack = `TypeError: ${secret}\n    at run (C:\\Users\\private-user\\cli.js:42:7)\n    at main (file:///home/private-user/cli.js?${secret}:90:3)`;
  await Sentry.withScope(async (scope) => {
    scope.setUser({ email: secret });
    scope.setExtra('input', secret);
    scope.setTag('profile', secret);
    scope.setContext('process', { argv: [secret], env: { TOKEN: secret } });
    scope.addBreadcrumb({ message: secret });
    reportUnexpectedError(error);
  });
  await flushTelemetry();

  expect(Sentry.flush).toHaveBeenCalledWith(2_000);
  expect(recorded.envelopes).toHaveLength(1);
  const serialized = JSON.stringify(recorded.envelopes);
  expect(serialized).not.toContain(secret);
  expect(serialized).not.toContain('private-user');
  const envelope = recorded.envelopes[0] as [unknown, [[unknown, Sentry.ErrorEvent]]];
  expect(envelope[1][0][1]).toMatchObject({
    release: `@accessdurin/cli@${version}`,
    tags: { component: 'cli', platform: process.platform, arch: process.arch },
    exception: {
      values: [
        {
          type: 'TypeError',
          value: 'Durin CLI operation failed',
          stacktrace: {
            frames: [
              { filename: 'cli.js', function: 'main', lineno: 90, colno: 3 },
              { filename: 'cli.js', function: 'run', lineno: 42, colno: 7 },
            ],
          },
        },
      ],
    },
  });
});

it('ignores expected CLI and argument errors without capturing or flushing', () => {
  const errors = [
    new CliError('Authorization required'),
    Object.assign(new TypeError('Unknown option'), { code: 'ERR_PARSE_ARGS_UNKNOWN_OPTION' }),
    Object.assign(new TypeError('Missing value'), { code: 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' }),
  ];
  errors.forEach(reportUnexpectedError);
  expect(Sentry.captureException).not.toHaveBeenCalled();
  expect(Sentry.flush).not.toHaveBeenCalled();
});

it('handles non-Error failures without serializing the thrown payload', async () => {
  reportUnexpectedError({ token: 'private-thrown-payload' });
  await flushTelemetry();
  expect(Sentry.captureException).toHaveBeenLastCalledWith(expect.any(Error));
  expect(recorded.envelopes).toHaveLength(1);
  expect(JSON.stringify(recorded.envelopes)).not.toContain('private-thrown-payload');
});

it('preserves CLI behavior when initialization, capture, or delivery fails', async () => {
  vi.mocked(Sentry.init).mockImplementationOnce(() => {
    throw new Error('Initialization failed');
  });
  expect(initializeTelemetry).not.toThrow();
  vi.mocked(Sentry.captureException).mockImplementationOnce(() => {
    throw new Error('Capture failed');
  });
  expect(() => reportUnexpectedError(new Error('capture'))).not.toThrow();
  vi.mocked(Sentry.flush).mockRejectedValueOnce(new Error('Offline'));
  await expect(flushTelemetry()).resolves.toBeUndefined();
  vi.mocked(Sentry.flush).mockResolvedValueOnce(false);
  await expect(flushTelemetry()).resolves.toBeUndefined();
  vi.mocked(Sentry.startSpan).mockImplementationOnce(() => {
    throw new Error('Tracing unavailable');
  });
  const action = vi.fn<() => Promise<string>>().mockResolvedValue('completed');
  await expect(traceCommand(action)).resolves.toBe('completed');
  expect(action).toHaveBeenCalledExactlyOnceWith();
  const failure = new Error('Command failed');
  action.mockClear().mockRejectedValueOnce(failure);
  await expect(traceCommand(action)).rejects.toBe(failure);
  expect(action).toHaveBeenCalledExactlyOnceWith();
});

it('profiles command spans and removes sensitive span attributes and native frame paths', async () => {
  const secret = 'private-profile-input';
  const result = await traceCommand(async () => {
    Sentry.getActiveSpan()!.setAttribute('tool.input', secret);
    const until = performance.now() + 75;
    let digest = secret;
    while (performance.now() < until) digest = createHash('sha256').update(digest).digest('hex');
    return Promise.resolve(digest);
  });
  expect(result).toMatch(/^[a-f0-9]{64}$/u);
  await flushTelemetry();
  const serialized = JSON.stringify(recorded.envelopes);
  expect(serialized).not.toContain(secret);
  expect(serialized).not.toContain(process.cwd());
  expect(serialized).not.toContain('abs_path');
  expect(serialized).not.toContain('debug_meta');
  expect(serialized).toContain('"profile_chunk"');
  expect(serialized).toContain('"durin command"');
  expect(serialized).toContain('"cli.command"');
  expect(serialized).toContain('sentry.profiler_id');
  expect(serialized).toContain('"filename"');
});

it.each(['1', 'true', 'TRUE'])('sends nothing when DO_NOT_TRACK=%s', async (value) => {
  vi.stubEnv('DO_NOT_TRACK', value);
  initializeTelemetry();
  expect(Sentry.getClient()!.getOptions().enabled).toBe(false);
  reportUnexpectedError(new Error('opted out'));
  await traceCommand(() => Promise.resolve('completed'));
  await flushTelemetry();
  expect(recorded.envelopes).toHaveLength(0);
});
