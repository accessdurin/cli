// Drives the TypeScript CLI's credential library (@napi-rs/keyring) for the
// Go cross-compatibility test in compat_test.go. It opens entries exactly as
// src/credentials.ts does: new AsyncEntry(service, key,
// { linux: { store: 'secret-service' } }).
//
// Protocol: one JSON request on stdin, one JSON response on stdout. Values
// travel over stdin/stdout, never argv.
//   request:  {"op": "get" | "set" | "delete", "service": "...", "key": "...", "value": "..."}
//   response: {"value": string | null} for get, {"deleted": boolean} for
//             delete, {} for set, or {"error": "..."} if the call rejects.
//
// Run the Go tests that use it (Linux: inside an unlocked gnome-keyring, with
// secret-tool installed; macOS and Windows: against the real store):
//
//   DURIN_TEST_KEYRING=1 \
//   DURIN_TS_KEYRING=/path/to/cli/node_modules/@napi-rs/keyring/index.js \
//   go test ./internal/credentials/ -run 'Native|Compat' -v
//
// Headless Linux: run the same command inside `dbus-run-session --` after
// starting an unlocked keyring with a throwaway HOME:
//
//   export HOME=$(mktemp -d)
//   printf test | gnome-keyring-daemon --unlock --replace --components=secrets
//
// and wait until the daemon has a default collection (`dbus-send --session
// --print-reply --dest=org.freedesktop.secrets /org/freedesktop/secrets
// org.freedesktop.Secret.Service.ReadAlias string:default` names a
// collection); before that, both implementations correctly fail to write.
import { pathToFileURL } from 'node:url';

const modulePath = process.env.DURIN_TS_KEYRING;
if (!modulePath) throw new Error('DURIN_TS_KEYRING must name @napi-rs/keyring/index.js');

let input = '';
for await (const chunk of process.stdin) input += chunk;
const request = JSON.parse(input);

const { AsyncEntry } = await import(pathToFileURL(modulePath).href);

let response;
try {
  const entry = new AsyncEntry(request.service, request.key, { linux: { store: 'secret-service' } });
  if (request.op === 'get') response = { value: (await entry.getPassword()) ?? null };
  else if (request.op === 'set') response = (await entry.setPassword(request.value), {});
  else if (request.op === 'delete') response = { deleted: await entry.deleteCredential() };
  else throw new Error(`unknown op ${request.op}`);
} catch (error) {
  response = { error: String(error?.message ?? error) };
}
process.stdout.write(JSON.stringify(response));
