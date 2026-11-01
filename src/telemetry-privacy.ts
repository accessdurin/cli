import * as Sentry from '@sentry/node';
import { version } from '../package.json';

type TransportFactory = NonNullable<Sentry.NodeOptions['transport']>;
type Envelope = Parameters<ReturnType<TransportFactory>['send']>[0];
type EnvelopeItem = Envelope[1][number];
type ProfileChunk = Extract<EnvelopeItem, [{ type: 'profile_chunk' }, unknown]>[1];
type StreamedSpan = Parameters<Parameters<typeof Sentry.withStreamedSpan>[0]>[0];

const ERROR_TYPES = new Set([
  'Error',
  'TypeError',
  'RangeError',
  'ReferenceError',
  'SyntaxError',
  'URIError',
  'EvalError',
  'AggregateError',
]);
const SPAN_ATTRIBUTES = [
  'sentry.op',
  'sentry.origin',
  'sentry.sample_rate',
  'sentry.profile_id',
  'sentry.profiler_id',
  'sentry.release',
  'sentry.environment',
  'sentry.sdk.name',
  'sentry.sdk.version',
  'thread.id',
  'thread.name',
] as const;

const keepFields = <T extends object, const K extends keyof T>(
  value: T,
  fields: readonly K[],
): Pick<T, K> =>
  Object.fromEntries(
    fields.filter((field) => Object.hasOwn(value, field)).map((field) => [field, value[field]]),
  ) as Pick<T, K>;

const filenameOnly = (path: string): string =>
  path.replaceAll('\\', '/').split(/[?#]/u)[0]?.split('/').at(-1) ?? '';

const safeFrame = (frame: Sentry.StackFrame): Sentry.StackFrame => ({
  ...keepFields(frame, ['function', 'lineno', 'colno', 'in_app']),
  ...(frame.filename === undefined ? {} : { filename: filenameOnly(frame.filename) }),
});
const errorType = (type = 'Error'): string => (ERROR_TYPES.has(type) ? type : 'Error');
const safeMechanism = (mechanism: Sentry.Exception['mechanism']) =>
  keepFields(mechanism ?? { type: 'generic', handled: true }, ['type', 'handled']);
const safeException = (exception: Sentry.Exception): Sentry.Exception => ({
  type: errorType(exception.type),
  value: 'Durin CLI operation failed',
  mechanism: safeMechanism(exception.mechanism),
  stacktrace: { frames: (exception.stacktrace?.frames ?? []).map(safeFrame) },
});
const safeTrace = (
  contexts: NonNullable<Sentry.ErrorEvent['contexts']> = {},
): NonNullable<Sentry.ErrorEvent['contexts']> => ({
  ...(contexts.trace === undefined
    ? {}
    : { trace: keepFields(contexts.trace, ['trace_id', 'span_id', 'parent_span_id']) }),
  ...(contexts.profile === undefined
    ? {}
    : { profile: keepFields(contexts.profile, ['profile_id', 'profiler_id']) }),
});

// Keep diagnostics useful without sending auth data, tool payloads, or local paths.
export const scrubEvent = (event: Sentry.ErrorEvent): Sentry.ErrorEvent => ({
  ...keepFields(event, ['event_id', 'timestamp', 'level', 'sdk']),
  type: event.type,
  platform: 'node',
  release: `@accessdurin/cli@${version}`,
  environment: 'production',
  tags: { component: 'cli', platform: process.platform, arch: process.arch },
  contexts: safeTrace(event.contexts),
  exception: { values: (event.exception?.values ?? []).map(safeException) },
});

export const scrubSpan = (span: StreamedSpan): StreamedSpan => ({
  ...keepFields(span, [
    'trace_id',
    'span_id',
    'parent_span_id',
    'start_timestamp',
    'end_timestamp',
    'status',
    'is_segment',
  ]),
  name: 'durin command',
  attributes: keepFields(span.attributes, SPAN_ATTRIBUTES),
});

const safeProfileFrame = (frame: Sentry.StackFrame & { file?: string }) => ({
  ...safeFrame(frame),
  ...(frame.file === undefined ? {} : { file: filenameOnly(frame.file) }),
});
const scrubProfileChunk = (chunk: ProfileChunk): ProfileChunk => ({
  ...keepFields(chunk, [
    'chunk_id',
    'profiler_id',
    'client_sdk',
    'platform',
    'version',
    'release',
    'environment',
    'measurements',
  ]),
  profile: {
    ...keepFields(chunk.profile, ['samples', 'stacks', 'thread_metadata']),
    frames: chunk.profile.frames.map(safeProfileFrame),
  },
});
const scrubItem = (item: EnvelopeItem): EnvelopeItem =>
  item[0].type === 'profile_chunk' ? [item[0], scrubProfileChunk(item[1] as ProfileChunk)] : item;

// Profiles bypass beforeSend, so scrub their frame paths at the transport boundary.
export const makeTelemetryTransport: TransportFactory = (options) => {
  const transport = Sentry.makeNodeTransport(options);
  return {
    send: (envelope) => transport.send([envelope[0], envelope[1].map(scrubItem)] as Envelope),
    flush: (timeout) => transport.flush(timeout),
  };
};
