// Runs the real @modelcontextprotocol SDK (2.3.1, from the CLI's node_modules) against the
// scenarios in ./scenarios and records what it does into ./fixtures:
//   - every HTTP request (method, URL, headers, body, redirect mode), in order;
//   - every provider call with its arguments (canonicalized: object keys sorted);
//   - each step's result or error, and the SDK's console.warn output.
// The Go tests replay the same scenarios against the port and compare. `go test` never runs
// Node; re-run this script (node record.mjs) after changing a scenario or upgrading the SDK.
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  auth,
  Client,
  discoverAuthorizationServerMetadata,
  discoverOAuthProtectedResourceMetadata,
  StreamableHTTPClientTransport,
} from '/home/user/cli/node_modules/@modelcontextprotocol/client/dist/index.mjs';

const here = dirname(fileURLToPath(import.meta.url));
let warnings = [];
console.warn = (...args) => warnings.push(args.map(String).join(' '));
console.debug = () => {};

const canonical = (value) => {
  if (Array.isArray(value)) return value.map(canonical);
  if (value instanceof URL) return value.href;
  if (value && typeof value === 'object') {
    const out = {};
    for (const key of Object.keys(value).sort()) if (value[key] !== undefined) out[key] = canonical(value[key]);
    return out;
  }
  return value;
};

/** Replaces the random PKCE values so fixtures are stable; the Go test checks them separately. */
const normalize = (text) =>
  text
    .replace(/code_challenge=[A-Za-z0-9_-]{43}/g, 'code_challenge=<challenge>')
    .replace(/code_verifier=(?:[A-Za-z0-9._-]|%7E){43}/g, 'code_verifier=<verifier>');

const errorRecord = (error) => ({
  name: error?.constructor?.name ?? typeof error,
  message: String(error?.message ?? error),
  ...(error?.code !== undefined ? { code: error.code } : {}),
});

function makeFetch(scenario, requests) {
  const counters = new Map();
  return async (input, init = {}) => {
    const url = new URL(String(input));
    const headers = {};
    for (const [k, v] of new Headers(init.headers ?? {})) headers[k] = v;
    const body = init.body === undefined || init.body === null ? null : String(init.body);
    requests.push({
      method: init.method ?? 'GET',
      url: normalize(url.href),
      headers: Object.fromEntries(Object.entries(headers).map(([k, v]) => [k, normalize(v)]).sort()),
      body: body === null ? null : normalize(body),
      redirect: init.redirect ?? null,
    });
    let rpc;
    if (body) {
      try {
        rpc = JSON.parse(body);
      } catch {}
    }
    const index = scenario.routes.findIndex(
      (r) =>
        r.method === (init.method ?? 'GET') &&
        r.path === url.pathname &&
        (r.host === undefined || r.host === url.hostname) &&
        (r.rpcMethod === undefined || rpc?.method === r.rpcMethod),
    );
    if (index === -1) return new Response('no route', { status: 599 });
    const route = scenario.routes[index];
    const n = counters.get(index) ?? 0;
    counters.set(index, n + 1);
    const spec = route.responses[Math.min(n, route.responses.length - 1)];
    const id = rpc?.id;
    const sub = (text) => text.replaceAll('"$id"', JSON.stringify(id)).replaceAll('$id', JSON.stringify(id));
    const respHeaders = { ...(spec.headers ?? {}) };
    let payload = null;
    if (spec.json !== undefined) {
      payload = sub(JSON.stringify(spec.json));
      respHeaders['content-type'] ??= 'application/json';
    } else if (spec.sse !== undefined) {
      payload = spec.sse.map(sub).join('');
      respHeaders['content-type'] ??= 'text/event-stream';
    } else if (spec.text !== undefined) {
      payload = spec.text;
    }
    return new Response(payload, { status: spec.status, headers: respHeaders });
  };
}

function makeProvider(spec, calls) {
  const state = {
    tokens: spec.tokens ?? undefined,
    client: spec.clientInformation ?? undefined,
    discovery: undefined,
    verifier: undefined,
  };
  const log = (name, ...args) => calls.push([name, ...args.map(canonical)]);
  return {
    get redirectUrl() {
      return spec.redirectUrl ?? undefined;
    },
    get clientMetadata() {
      return spec.clientMetadata;
    },
    state() {
      log('state');
      if (spec.stateThrows) throw new Error(spec.stateThrows);
      return spec.state ?? '';
    },
    redirectToAuthorization(url) {
      log('redirectToAuthorization', normalize(url.href));
    },
    saveCodeVerifier(verifier) {
      log('saveCodeVerifier', '<verifier>');
      state.verifier = verifier;
    },
    codeVerifier() {
      log('codeVerifier');
      return state.verifier ?? 'stored-verifier';
    },
    clientInformation(ctx) {
      log('clientInformation', ctx ?? null);
      return state.client;
    },
    saveClientInformation(info, ctx) {
      log('saveClientInformation', info, ctx ?? null);
      state.client = info;
    },
    tokens(ctx) {
      log('tokens', ctx ?? null);
      return state.tokens;
    },
    saveTokens(tokens, ctx) {
      log('saveTokens', tokens, ctx ?? null);
      state.tokens = tokens;
    },
    validateResourceURL(serverUrl, resource) {
      log('validateResourceURL', String(serverUrl), resource ?? null);
      return new URL(String(serverUrl));
    },
    saveDiscoveryState(s) {
      log('saveDiscoveryState', s);
      state.discovery = s;
    },
    discoveryState() {
      log('discoveryState');
      return state.discovery;
    },
    invalidateCredentials(scope) {
      log('invalidateCredentials', scope);
      if (scope === 'all' || scope === 'tokens') state.tokens = undefined;
      if (scope === 'all' || scope === 'client') state.client = undefined;
      if (scope === 'all' || scope === 'verifier') state.verifier = undefined;
      if (scope === 'all' || scope === 'discovery') state.discovery = undefined;
    },
  };
}

async function run(scenario) {
  const requests = [];
  const calls = [];
  const transportErrors = [];
  const fetchFn = makeFetch(scenario, requests);
  const provider = makeProvider(scenario.provider ?? {}, calls);
  const steps = [];
  let client;
  let transport;
  const ensureTransport = () => {
    if (transport) return transport;
    const t = scenario.transport;
    transport = new StreamableHTTPClientTransport(new URL(t.url), {
      authProvider: provider,
      fetch: fetchFn,
      requestInit: t.requestInit,
      onInsufficientScope: t.onInsufficientScope,
    });
    return transport;
  };
  for (const step of scenario.steps) {
    const at = requests.length;
    const callsAt = calls.length;
    warnings = [];
    let record;
    try {
      switch (step.op) {
        case 'auth': {
          const result = await auth(provider, {
            serverUrl: step.serverUrl,
            fetchFn,
            ...(step.authorizationCode !== undefined ? { authorizationCode: step.authorizationCode } : {}),
            ...(step.iss !== undefined ? { iss: step.iss } : {}),
          });
          record = { result };
          break;
        }
        case 'discoverAS': {
          const meta = await discoverAuthorizationServerMetadata(step.url, { fetchFn });
          record = { result: meta === undefined ? null : JSON.stringify(meta) };
          break;
        }
        case 'discoverPRM': {
          const meta = await discoverOAuthProtectedResourceMetadata(step.url, undefined, fetchFn);
          record = { result: JSON.stringify(meta) };
          break;
        }
        case 'connect': {
          const c = scenario.client;
          client = new Client({ name: c.name, version: c.version }, c.listMaxPages !== undefined ? { listMaxPages: c.listMaxPages } : undefined);
          const t = ensureTransport();
          await client.connect(t, { timeout: 15000 });
          record = { result: { sessionId: t.sessionId ?? null, protocolVersion: t.protocolVersion ?? null } };
          break;
        }
        case 'listTools':
          record = { result: JSON.stringify(await client.listTools(undefined, { timeout: 15000 })) };
          break;
        case 'callTool':
          record = { result: JSON.stringify(await client.callTool(step.params, { timeout: 60000 })) };
          break;
        case 'send': {
          const t = ensureTransport();
          t.onerror = (e) => transportErrors.push(errorRecord(e));
          const received = [];
          t.onmessage = (m) => received.push(JSON.stringify(m));
          await t.start();
          await t.send(step.message);
          record = { result: received };
          break;
        }
        case 'close':
          await client.close();
          record = { result: null };
          break;
        default:
          throw new Error(`unknown op ${step.op}`);
      }
    } catch (error) {
      record = { error: errorRecord(error) };
    }
    // Let fire-and-forget work (the standalone GET) settle before the next step.
    await new Promise((resolve) => setTimeout(resolve, 50));
    steps.push({ op: step.op, ...record, requests: requests.slice(at), calls: calls.slice(callsAt), warnings });
  }
  return { steps, transportErrors };
}

const only = process.argv[2];
for (const file of readdirSync(join(here, 'scenarios')).sort()) {
  if (only && !file.startsWith(only)) continue;
  const scenario = JSON.parse(readFileSync(join(here, 'scenarios', file), 'utf8'));
  const fixture = await run(scenario);
  writeFileSync(join(here, 'fixtures', file), `${JSON.stringify(fixture, null, 2)}\n`);
  console.log(`recorded ${file}`);
}
