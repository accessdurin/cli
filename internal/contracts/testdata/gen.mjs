// Regenerates fixtures.json from the TypeScript contracts (src/contracts) with the real zod:
//
//   node --experimental-strip-types --no-warnings internal/contracts/testdata/gen.mjs
//
// Each schema case records whether safeParse succeeded, JSON.stringify of its output, and its
// custom issues (the only issues the CLI shows). Each function case records the result or
// that the function threw.
import { register } from 'node:module';
import { writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

register(new URL('./ts-loader.mjs', import.meta.url));
const src = new URL('../../../src/contracts/', import.meta.url);
const load = (name) => import(new URL(name, src).href);
const profile = await load('cli-profile.ts');
const url = await load('cli-url.ts');
const host = await load('cli-host.ts');
const mcp = await load('cli-mcp.ts');
const discovery = await load('mcp-discovery.ts');
const result = await load('mcp-result.ts');
const endpoint = await load('mcp-endpoint.ts');
const auth = await load('cli-auth.ts');
const onboarding = await load('onboarding.ts');
const account = await load('account.ts');
const bootstrap = await load('personal-bootstrap.ts');

const schemas = {
  cliProfileSchema: profile.cliProfileSchema,
  cliProfileScopeSchema: profile.cliProfileScopeSchema,
  cliToolCallSchema: mcp.cliToolCallSchema,
  cliMcpNegotiationSchema: mcp.cliMcpNegotiationSchema,
  mcpToolsPageSchema: discovery.mcpToolsPageSchema,
  mcpToolsListSchema: discovery.mcpToolsListSchema,
  mcpToolResultSchema: result.mcpToolResultSchema,
  mcpEndpointSchema: endpoint.mcpEndpointSchema,
  cliAuthConfigSchema: auth.cliAuthConfigSchema,
  deviceChallengeSchema: auth.deviceChallengeSchema,
  deviceSessionSchema: auth.deviceSessionSchema,
  authorizationCallbackSchema: auth.authorizationCallbackSchema,
  onboardingStatusSchema: onboarding.onboardingStatusSchema,
  onboardingAnswerSchema: onboarding.onboardingAnswerSchema,
  sessionSchema: account.sessionSchema,
  organizationCreateSchema: account.organizationCreateSchema,
  personalBootstrapSchema: bootstrap.personalBootstrapSchema,
};

const hex = (c) => c.repeat(64);
const personal = `/mcp/u/${hex('a')}`;
const production = {
  appOrigin: 'https://app.getdurin.com',
  accountIssuer: 'https://auth.getdurin.com/',
  accountClientId: 'client',
  principalId: 'user_one',
  workosOrganizationId: 'org_wos',
  organizationId: 'org',
  environment: 'production',
  resource: `https://mcp.getdurin.com${personal}`,
  mcpIssuer: 'https://auth.getdurin.com',
  development: false,
};
const staging = {
  ...production,
  appOrigin: 'https://moria.app.getdurin.com',
  accountIssuer: 'https://resilient-tiger-64-staging.authkit.app/',
  resource: `https://moria.mcp.getdurin.com${personal}`,
  mcpIssuer: 'https://resilient-tiger-64-staging.authkit.app',
};
const selfHosted = {
  ...production,
  appOrigin: 'https://app.example.test',
  accountIssuer: 'https://app.example.test/',
  resource: `https://app.example.test${personal}`,
  mcpIssuer: 'https://app.example.test/',
};
const local = {
  ...production,
  appOrigin: 'http://127.0.0.1:5270',
  accountIssuer: 'http://127.0.0.1:5270/',
  resource: `http://127.0.0.1:5272${personal}`,
  mcpIssuer: 'http://127.0.0.1:5272/',
  development: true,
};
const withId = (scope) => ({ ...scope, version: 1, id: hex('b') });
const scopes = [
  production,
  staging,
  selfHosted,
  local,
  { ...local, development: false },
  { ...selfHosted, accountIssuer: 'https://tenant-1.authkit.app' },
  { ...selfHosted, accountIssuer: 'https://api.workos.com/' },
  { ...selfHosted, accountIssuer: 'https://a.b.authkit.app' },
  { ...production, mcpIssuer: 'https://evil.example' },
  { ...production, mcpIssuer: 'https://auth.getdurin.com:443/' },
  { ...production, mcpIssuer: 'https://AUTH.getdurin.com/' },
  { ...production, mcpIssuer: 'https://auth.getdurin.com/x' },
  { ...production, accountIssuer: 'https://api.workos.com/' },
  { ...production, resource: `https://evil.getdurin.com${personal}` },
  { ...production, resource: `https://127.0.0.1:8443${personal}` },
  { ...production, resource: `https://mcp.getdurin.com/mcp/u/short` },
  { ...production, appOrigin: 'https://app.getdurin.com/' },
  { ...production, appOrigin: 'https://app.getdurin.com.' },
  { ...production, appOrigin: ' https://app.getdurin.com ' },
  { ...production, resource: ` https://mcp.getdurin.com${personal}\n` },
  { ...production, accountIssuer: 'not a url' },
  { ...production, accountIssuer: 'foo://a%zz/' },
  { ...production, resource: `foo://a%zz${personal}` },
  { ...production, principalId: '' },
  { ...production, principalId: 7 },
  { ...production, environment: 'staging' },
  { ...production, extra: true },
  { ...staging, mcpIssuer: 'https://evil-tiger-99-staging.authkit.app/' },
  { ...staging, accountIssuer: 'https://auth.getdurin.com/' },
  { ...local, resource: `http://localhost:5272${personal}` },
  { ...local, resource: `http://[::1]:5272${personal}` },
  { ...local, resource: `http://evil.example${personal}` },
  { ...selfHosted, resource: `https://other.example${personal}` },
  { ...selfHosted, mcpIssuer: 'https://user:pw@app.example.test/' },
  { ...selfHosted, mcpIssuer: 'https://app.example.test/?q' },
  // #9: saved issuers are checked as written.
  { ...production, accountIssuer: 'https://auth.getdurin.com' },
  {
    ...production,
    accountIssuer: 'https://auth.getdurin.com',
    mcpIssuer: 'https://auth.getdurin.com/',
  },
  { ...production, accountIssuer: ' https://auth.getdurin.com' },
  { ...production, accountIssuer: 'https://auth.getdurin.com\t' },
  { ...production, accountIssuer: 'https://auth.getdurin.com\\' },
  { ...production, accountIssuer: 'https://auth.getdurin.com.' },
  {
    ...production,
    accountIssuer: 'https://auth.getdurin.com',
    mcpIssuer: 'https://auth.getdurin.com//',
  },
  { ...staging, accountIssuer: 'https://resilient-tiger-64-staging.authkit.app' },
  { ...selfHosted, mcpIssuer: 'HTTPS://app.example.test/' },
  { ...selfHosted, mcpIssuer: 'https://app.example.test:443/' },
  { ...selfHosted, mcpIssuer: 'https://app.example.test//' },
  { ...selfHosted, mcpIssuer: 'https://app.example.test/\u0000' },
  { ...selfHosted, mcpIssuer: 'https://app.example.test' },
  { ...selfHosted, accountIssuer: 'https://tenant-1.authkit.app:443' },
  { ...selfHosted, accountIssuer: 'https://TENANT-1.AUTHKIT.APP/' },
];
const json = (value) => JSON.stringify(value);
const schemaCases = [
  ...scopes.map((scope) => ['cliProfileScopeSchema', json(scope)]),
  ...scopes.map((scope) => ['cliProfileSchema', json(withId(scope))]),
  ['cliProfileSchema', json({ ...production, version: 2, id: hex('b') })],
  ['cliProfileSchema', json({ ...production, version: 1, id: 'nope' })],
  ['cliProfileSchema', json({ ...production, version: 1 })],
  ['cliProfileSchema', '[]'],
  ['cliProfileSchema', 'null'],
  [
    'cliToolCallSchema',
    '{"name":"tool","arguments":{"a":[1,{"__proto__":{"x":1}}],"__proto__":2},"idempotencyKey":"k-1"}',
  ],
  [
    'cliToolCallSchema',
    '{"name":"tool","arguments":{},"idempotencyKey":"k","approvalId":"a/b:c.d"}',
  ],
  ['cliToolCallSchema', '{"name":"-tool","arguments":{},"idempotencyKey":"k"}'],
  ['cliToolCallSchema', '{"name":"tool","arguments":[],"idempotencyKey":"k"}'],
  ['cliToolCallSchema', '{"name":"tool","arguments":{},"idempotencyKey":"k k"}'],
  ['cliToolCallSchema', `{"name":"tool","arguments":{},"idempotencyKey":"${'k'.repeat(129)}"}`],
  ['cliToolCallSchema', '{"name":"tool","arguments":{},"idempotencyKey":"k","extra":1}'],
  ['cliMcpNegotiationSchema', '{"id":1,"result":{"protocolVersion":"2025","x":1},"jsonrpc":"2.0"}'],
  ['cliMcpNegotiationSchema', '{"id":null,"result":{"protocolVersion":"2025"}}'],
  [
    'mcpToolsPageSchema',
    '{"tools":[{"zz":1,"name":"b","inputSchema":{"type":"object","properties":{},"__proto__":{}},"description":"d","annotations":{"readOnlyHint":true},"outputSchema":true}],"nextCursor":"c","extra":[1]}',
  ],
  ['mcpToolsPageSchema', '{"tools":[],"nextCursor":null}'],
  ['mcpToolsPageSchema', '{"tools":[{"name":"","inputSchema":{"type":"object"}}]}'],
  ['mcpToolsPageSchema', '{"tools":[{"name":"x","inputSchema":{"type":"string"}}]}'],
  [
    'mcpToolsListSchema',
    '{"tools":[{"name":"a","inputSchema":{"type":"object"}},{"name":"b","title":"B","inputSchema":{"type":"object"}}]}',
  ],
  [
    'mcpToolsListSchema',
    '{"tools":[{"name":"a","inputSchema":{"type":"object"}},{"name":"a","inputSchema":{"type":"object"}}]}',
  ],
  ['mcpToolsListSchema', '{"tools":[],"nextCursor":"more"}'],
  ['mcpToolsListSchema', '{"tools":[],"nextCursor":null}'],
  [
    'mcpToolResultSchema',
    '{"content":[{"type":"text","text":"hi","extra":1},{"type":"image","data":"aGk=","mimeType":"image/png"},{"type":"resource","resource":{"uri":"u","text":"t"}},{"type":"resource_link","uri":"u","name":"n","size":3}],"structuredContent":{"a":1},"isError":false,"other":1}',
  ],
  [
    'mcpToolResultSchema',
    '{"content":[{"type":"text","text":"x"}],"_meta":{"io.durin/control":{"idempotencyKey":"k"}}}',
  ],
  [
    'mcpToolResultSchema',
    '{"content":[{"type":"resource","resource":{"uri":"u","text":"t","blob":"aGk="}}]}',
  ],
  [
    'mcpToolResultSchema',
    '{"content":[{"type":"image","data":"not base64!","mimeType":"image/png"}]}',
  ],
  ['mcpToolResultSchema', `{"content":[{"type":"text","text":"${'x'.repeat(16_385)}"}]}`],
  [
    'mcpToolResultSchema',
    `{"content":[{"type":"text","text":"${'x'.repeat(16_000)}"},{"type":"text","text":"${'x'.repeat(16_000)}"},{"type":"text","text":"${'x'.repeat(16_000)}"},{"type":"text","text":"${'x'.repeat(16_000)}"},{"type":"text","text":"${'x'.repeat(16_000)}"}]}`,
  ],
  ['mcpToolResultSchema', '{"content":[]}'],
  ['mcpEndpointSchema', '{"status":"ready","url":"https://mcp.example/mcp/u/x"}'],
  ['mcpEndpointSchema', '{"status":"unconfigured","url":null}'],
  ['mcpEndpointSchema', '{"status":"ready","url":null}'],
  ['mcpEndpointSchema', '{"status":"gone","url":null}'],
  [
    'cliAuthConfigSchema',
    '{"clientId":"c","issuer":"https://i","providerOrigin":"https://p","appOrigin":"https://a"}',
  ],
  [
    'cliAuthConfigSchema',
    '{"clientId":"c","issuer":"https://i","providerOrigin":"https://p","appOrigin":"https://a","x":1}',
  ],
  [
    'cliAuthConfigSchema',
    '{"clientId":"c","issuer":" https://i ","providerOrigin":"https://p","appOrigin":"https://a"}',
  ],
  [
    'cliAuthConfigSchema',
    '{"clientId":"c","issuer":"","providerOrigin":"https://p","appOrigin":"https://a"}',
  ],
  [
    'cliAuthConfigSchema',
    '{"clientId":"c","issuer":"nope","providerOrigin":"https://p","appOrigin":"https://a"}',
  ],
  [
    'deviceChallengeSchema',
    '{"device_code":"d","user_code":"u","verification_uri":"https://v","expires_in":600,"x":1}',
  ],
  [
    'deviceChallengeSchema',
    '{"device_code":"d","user_code":"u","verification_uri":"https://v","expires_in":600,"interval":61}',
  ],
  [
    'deviceChallengeSchema',
    '{"device_code":"d","user_code":"u","verification_uri":"https://v","expires_in":1.5}',
  ],
  ['deviceSessionSchema', '{"access_token":"a","refresh_token":"r","user":{"id":"u"}}'],
  [
    'deviceSessionSchema',
    '{"access_token":"a","refresh_token":"r","organization_id":"o","user":{"id":"u"}}',
  ],
  [
    'deviceSessionSchema',
    '{"access_token":"a","refresh_token":"r","organization_id":"","user":{"id":"u"}}',
  ],
  ['authorizationCallbackSchema', '{"state":"s","code":"c","iss":"https://i"}'],
  ['authorizationCallbackSchema', '{"state":"s","error":"access_denied"}'],
  ['authorizationCallbackSchema', '{"state":"s","code":"c","error":"e"}'],
  ['authorizationCallbackSchema', '{"state":"s"}'],
  [
    'onboardingStatusSchema',
    '{"organizationId":null,"journey":"organization_creator","next":"/onboarding","allowed":true,"profile":{"organizationId":"o","creatorId":"c","enrollment":"required","title":"  CTO ","headcount":5,"source":null,"otherSource":null,"paymentReady":false,"completedAt":null,"version":2},"step":"source","tenantReady":false}',
  ],
  [
    'onboardingStatusSchema',
    '{"organizationId":"o","journey":"existing","next":"/connections","allowed":true,"profile":null,"step":"complete","tenantReady":true}',
  ],
  ['onboardingAnswerSchema', '{"step":"title","version":1,"value":"  CEO  "}'],
  ['onboardingAnswerSchema', '{"step":"source","version":1,"value":"other"}'],
  ['onboardingAnswerSchema', '{"step":"source","version":1,"value":"other","other":" a friend "}'],
  ['onboardingAnswerSchema', '{"step":"headcount","version":1,"value":0}'],
  ['onboardingAnswerSchema', '{"step":"payment","version":1}'],
  [
    'sessionSchema',
    '{"mode":"workos","principalId":"p","name":"n","email":"e","role":"admin","organizationId":"o","organizations":[{"id":"o","name":"O","x":1}],"onboarding":{"organizationId":"o","journey":"existing","next":"/connections","allowed":true}}',
  ],
  [
    'organizationCreateSchema',
    '{"name":"Acme","idempotencyKey":"10000000-0000-4000-8000-000000000001"}',
  ],
  [
    'organizationCreateSchema',
    '{"name":"Acme","idempotencyKey":"10000000-0000-0000-8000-000000000001"}',
  ],
  [
    'personalBootstrapSchema',
    `{"organization":{"id":"o","name":"O","environment":"production"},"membership":{"principalId":"p","role":"member","active":true,"revocationEpoch":0},"entitlements":{"organizationId":"o","package":"standard"},"session":{"mode":"dev","principalId":"p","name":"n","email":"e","role":"member","organizationId":"o","organizations":[]},"mcp":{"status":"needs_setup","url":"https://x/mcp/u/${hex('a')}"},"settings":{"retention":"metadata_only","environment":"production"}}`,
  ],
  [
    'personalBootstrapSchema',
    '{"organization":{"id":"o","name":"O","environment":"production"},"membership":{"principalId":"p","role":"member","active":true,"revocationEpoch":0},"extra":1}',
  ],
];

const describe = (issue) =>
  issue.code === 'custom' ? { message: issue.message, path: issue.path } : null;
const run = (name, input) => {
  let parsed;
  try {
    parsed = schemas[name].safeParse(JSON.parse(input));
  } catch (error) {
    return { schema: name, input, threw: error.constructor.name };
  }
  if (parsed.success) return { schema: name, input, ok: true, output: JSON.stringify(parsed.data) };
  return {
    schema: name,
    input,
    ok: false,
    custom: parsed.error.issues.map(describe).filter(Boolean),
  };
};
const attempt = (fn) => {
  try {
    return { result: fn() ?? null };
  } catch (error) {
    return { threw: error.constructor.name };
  }
};

const urls = [
  'https://example.com',
  'https://example.com/',
  'http://example.com',
  'http://127.0.0.1:9',
  'http://localhost:1/x',
  'http://a.localhost',
  'http://[::1]:3',
  'http://[::]:3',
  'http://0.0.0.0',
  'http://[::ffff:127.0.0.1]',
  'http://[::ffff:7f00:1]',
  'http://127.1',
  'http://2130706433',
  'https://user@example.com',
  'https://example.com?x=1',
  'https://example.com?',
  'https://example.com#',
  'https://example.com#x',
  'https://example.com\n',
  'https://exa\rmple.com',
  'not a url',
  'https://EXAMPLE.com',
  'HTTP://LOCALHOST',
  'http://localhost.',
  'http://LocalHost.:8',
];
const hostsToCheck = [
  'localhost',
  'LOCALHOST.',
  '[::1]',
  '::1',
  '127.0.0.1',
  '0x7f.1',
  'example.com',
  '[::ffff:7f00:1]',
  '',
];
const hostScopes = [
  { appOrigin: 'https://app.getdurin.com', development: false },
  { appOrigin: 'https://moria.app.getdurin.com', development: false },
  { appOrigin: 'https://app.example.test', development: false },
  {
    appOrigin: 'https://app.example.test',
    development: false,
    accountIssuer: 'https://t.authkit.app',
  },
  { appOrigin: 'http://127.0.0.1:5270', development: true },
  { appOrigin: 'https://app.example.test.', development: false },
  { appOrigin: 'https://app.example.test:8443', development: false },
];
const hostUrls = [
  'https://mcp.getdurin.com/mcp/u/x',
  'https://auth.getdurin.com',
  'https://auth.getdurin.com/',
  'https://auth.getdurin.com:443',
  'https://api.workos.com',
  'https://api.workos.com/',
  'https://t.authkit.app',
  'https://app.example.test/',
  'https://app.example.test:8443/x',
  'http://127.0.0.1:5272/',
  'https://localhost/',
  'https://evil.example/',
  'nope',
  'foo://a%zz/',
  'HTTPS://app.example.test/',
  'https://app.example.test//',
  'https://app.example.test:443/',
  'https://app.example.test/x/',
  'https://app.example.test/\u0000',
  'https://app.example.test\\',
  'https://app.example.test /',
  'https://t.authkit.app:443',
  'https://T.AUTHKIT.APP/',
  'https://t.authkit.app:8443',
];
const functionCases = [];
const record = (fn, args, call) => {
  functionCases.push(Object.assign({ fn, args }, attempt(call)));
};
const allowlists = [
  { hosts: ['issuer.example.test'], development: false },
  { hosts: ['127.0.0.1'], development: false },
  { hosts: [], development: true },
];
for (const value of urls)
  for (const dev of [false, true])
    record('safeCliUrl', [value, dev], () => url.safeCliUrl(value, dev));
for (const value of hostsToCheck)
  record('loopbackCliHost', [value], () => url.loopbackCliHost(value));
for (const scope of hostScopes) {
  for (const role of host.cliHostRoles)
    for (const value of hostUrls)
      record('untrustedCliHost', [role, value, scope], () =>
        host.untrustedCliHost(role, value, scope),
      );
  for (const role of ['mcpIssuer', 'accountIssuer', 'resource'])
    for (const value of hostUrls)
      record('canonicalIssuer', [role, value, scope], () =>
        host.canonicalIssuer(role, value, scope),
      );
  for (const role of ['mcpIssuer', 'accountIssuer'])
    for (const value of hostUrls)
      record('comparableIssuer', [role, value, scope], () =>
        host.comparableIssuer(role, value, scope),
      );
  record('deploymentHostAllowlist', [scope], () => host.deploymentHostAllowlist(scope));
}
for (const value of [...hostUrls, ...urls, 'https://h', 'https://h/', 'https://h//x', 'https://h?'])
  record('roundTripCliUrl', [value], () => url.roundTripCliUrl(value));
for (const allowlist of allowlists)
  for (const value of [
    ...hostUrls,
    'https://ISSUER.Example.test/token',
    'https://issuer.example.test:443/x',
  ])
    record('unlistedCliHost', [value, allowlist], () => host.unlistedCliHost(value, allowlist));

const fixtures = {
  schemas: schemaCases.map(([name, input]) => run(name, input)),
  functions: functionCases,
};
const output = new URL('./fixtures.json', import.meta.url);
writeFileSync(output, `${JSON.stringify(fixtures)}\n`);
execFileSync(fileURLToPath(new URL('../../../node_modules/.bin/prettier', import.meta.url)), [
  '--write',
  fileURLToPath(output),
]);
