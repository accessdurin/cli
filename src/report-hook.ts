import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { join } from 'node:path';
import type { Readable } from 'node:stream';
import { z } from 'zod';
import {
  LOCAL_TOOL_CALL_ARGUMENT_BYTES,
  localToolCallSchema,
  reportIdentifierSchema,
  type LocalToolCall,
} from './contracts/local-tool-calls.js';
import { endpointChecker } from './hook-checker.js';
import {
  localServerTransport,
  type LocalTransport,
  type McpConfigLocation,
} from './local-mcp-servers.js';
import { regularText } from './managed-files.js';
import { spoolCall, spoolDirectory } from './report-spool.js';
import { isReportFormat, observedMcpCall, type ObservedCall } from './tool-call-event.js';

export const reportingSchema = z
  .object({
    harness: z.string().regex(/^[a-z][a-z0-9-]{0,63}$/u),
    format: z.string().refine(isReportFormat),
    profileId: z.string().min(1).max(128),
    target: z.string(),
    command: z.string(),
    mcpConfig: z
      .object({
        configPath: z.string().min(1),
        format: z.enum(['jsonc', 'toml']),
        serverKey: z.string().min(1).max(64),
      })
      .strict(),
    installedAt: z.string(),
  })
  .strict();
export type ReportingInstallation = z.infer<typeof reportingSchema>;
export const reportingPath = (directory: string, harness: string) =>
  join(
    directory,
    'reporting',
    `${z
      .string()
      .regex(/^[a-z][a-z0-9-]{0,63}$/u)
      .parse(harness)}.json`,
  );
export async function readReporting(
  directory: string,
  harness: string,
): Promise<ReportingInstallation | null> {
  const raw = await regularText(reportingPath(directory, harness)).catch(() => null);
  return raw === null ? null : reportingSchema.parse(JSON.parse(raw));
}

const bytes = (text: string) => Buffer.byteLength(text, 'utf8');
const plainObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);
function encodedArguments(input: unknown) {
  const object = plainObject(input) ? input : input === undefined ? {} : { value: input };
  const text = JSON.stringify(object);
  return bytes(text) <= LOCAL_TOOL_CALL_ARGUMENT_BYTES
    ? { argumentsJson: text, argumentsTruncated: false }
    : { argumentsJson: '{}', argumentsTruncated: true };
}
const identifier = (value: string | undefined): string | null =>
  reportIdentifierSchema.safeParse(value).data ?? null;
/** A stable ID per agent call, so a hook that fires twice stores the call once. */
function reportId(harness: string, call: ObservedCall): string {
  if (!call.sessionId || !call.toolCallId) return randomUUID();
  const hex = createHash('sha256')
    .update([harness, call.sessionId, call.toolCallId].join('\0'))
    .digest('hex');
  const variant = ((Number.parseInt(hex[16] ?? '0', 16) & 0x3) | 0x8).toString(16);
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-5${hex.slice(13, 16)}-${variant}${hex.slice(17, 20)}-${hex.slice(20, 32)}`;
}
export function localToolCall(
  harness: string,
  call: ObservedCall,
  transport: LocalTransport,
): LocalToolCall | null {
  const parsed = localToolCallSchema.safeParse({
    reportId: reportId(harness, call),
    reporter: 'cli',
    agent: harness,
    sessionId: identifier(call.sessionId),
    turnId: identifier(call.turnId),
    toolCallId: identifier(call.toolCallId),
    mcpServer: call.server,
    tool: call.tool,
    transport,
    outcome: call.outcome,
    ...encodedArguments(call.input),
    observedAt: new Date().toISOString(),
  });
  return parsed.success ? parsed.data : null;
}

async function readEvent(input: Readable): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of input) {
    const part = Buffer.from(chunk);
    size += part.length;
    if (size > 1_048_576) return null;
    chunks.push(part);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8').replace(/^﻿/u, '')) as unknown;
}
export function launchFlush(input: { cliPath: string; directory: string; profileId: string }) {
  const child = spawn(
    process.execPath,
    [
      input.cliPath,
      'hooks',
      'flush',
      '--profile',
      input.profileId,
      '--config-dir',
      input.directory,
    ],
    { detached: true, stdio: 'ignore', windowsHide: true, shell: false },
  );
  child.on('error', () => {});
  child.unref();
}

export type ReportDependencies = Readonly<{
  endpointPresent?: (harness: string) => Promise<boolean>;
  flush?: (input: { cliPath: string; directory: string; profileId: string }) => void;
}>;
export type ReportOutcome = 'disabled' | 'endpoint' | 'ignored' | 'not_local' | 'queued';
const endpointInstalled = async (harness: string) => (await endpointChecker(harness)) !== null;

type Format = Parameters<typeof observedMcpCall>[0];
const observedCall = (installation: ReportingInstallation, raw: unknown) =>
  raw === null ? null : observedMcpCall(installation.format as Format, raw);
async function localCall(
  installation: ReportingInstallation,
  call: ObservedCall,
  agent: McpConfigLocation,
) {
  const format = installation.format as Format;
  const transport = await localServerTransport({ format, agent, ...call });
  if (!transport) return 'not_local' as const;
  return localToolCall(installation.harness, call, transport) ?? ('ignored' as const);
}
async function queuedCall(installation: ReportingInstallation, raw: unknown) {
  const call = observedCall(installation, raw);
  if (!call) return 'ignored' as const;
  return localCall(installation, call, installation.mcpConfig);
}
const withDefaults = (dependencies: ReportDependencies | undefined) => ({
  endpointPresent: endpointInstalled,
  flush: launchFlush,
  ...dependencies,
});

/**
 * Handles one post-tool hook event: when Endpoint does not cover this harness and
 * the call went to a local MCP server, it spools the call and starts a background
 * flush. It never blocks or fails the agent.
 */
export async function reportHook(
  input: { directory: string; harness: string; cliPath: string; stdin: Readable },
  dependencies?: ReportDependencies,
): Promise<ReportOutcome> {
  const resolved = withDefaults(dependencies);
  const installation = await readReporting(input.directory, input.harness);
  if (!installation) return 'disabled';
  if (await resolved.endpointPresent(input.harness)) return 'endpoint';
  const call = await queuedCall(installation, await readEvent(input.stdin));
  if (typeof call === 'string') return call;
  const { directory, cliPath } = input;
  await spoolCall(spoolDirectory(directory, installation.profileId), call);
  resolved.flush({ cliPath, directory, profileId: installation.profileId });
  return 'queued';
}
