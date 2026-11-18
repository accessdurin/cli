// Records what the SDK's OAuthTokensSchema / OAuthClientInformationSchema /
// OAuthClientInformationFullSchema accept and produce, so stored keyring entries parse the same
// way in the Go port. Run: node record.mjs > schemas.json
import { OAuthClientInformationFullSchema, OAuthClientInformationSchema, OAuthTokensSchema } from '/home/user/cli/node_modules/@modelcontextprotocol/core/dist/index.mjs';
const tokens = [
  { access_token: 'a', token_type: 'Bearer' },
  { token_type: 'Bearer', access_token: 'a', expires_in: '3600', refresh_token: 'r', issuer: 'https://i/', extra: 1 },
  { access_token: 'a', token_type: 'Bearer', expires_in: ' 0x10 ' },
  { access_token: 'a', token_type: 'Bearer', expires_in: null },
  { access_token: 'a', token_type: 'Bearer', expires_in: true },
  { access_token: 'a', token_type: 'Bearer', expires_in: [7] },
  { access_token: 'a', token_type: 'Bearer', expires_in: 'abc' },
  { access_token: 'a', token_type: 'Bearer', expires_in: '1e400' },
  { access_token: 'a', token_type: 'Bearer', issuer: 5, scope: 's', id_token: 'i' },
  { access_token: 'a' },
  { access_token: 1, token_type: 'Bearer' },
  { access_token: 'a', token_type: 'Bearer', scope: null },
];
const clients = [
  { client_id: 'c' },
  { client_id: 'c', client_secret: 's', client_id_issued_at: 1, client_secret_expires_at: 0, issuer: 'https://i/', redirect_uris: ['http://127.0.0.1/cb'], token_endpoint_auth_method: 'none' },
  { client_id: 'c', issuer: ['x'] },
  { client_id: 'c', client_id_issued_at: '1' },
  { client_secret: 's' },
];
const full = [
  { client_id: 'c', redirect_uris: [' https://x/cb\t'], logo_uri: '', jwks: null, scope: 'a b', grant_types: [], extra: 1 },
  { client_id: 'c' },
  { client_id: 'c', redirect_uris: ['javascript:alert(1)'] },
  { client_id: 'c', redirect_uris: ['http://127.0.0.1/cb'], tos_uri: 'not a url' },
  { redirect_uris: ['http://a/'], client_id: 'c', client_name: 'n', contacts: ['x'], software_id: 'id', jwks: { keys: [] }, application_type: 'native', issuer: 'i' },
];
const run = (schema, list) => list.map((v) => {
  const r = schema.safeParse(v);
  return { input: JSON.stringify(v), ok: r.success, output: r.success ? JSON.stringify(r.data) : null };
});
console.log(JSON.stringify({ tokens: run(OAuthTokensSchema, tokens), clients: run(OAuthClientInformationSchema, clients), full: run(OAuthClientInformationFullSchema, full) }, null, 1));
