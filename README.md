# Durin CLI

Run `durin` to choose login or signup, complete organization onboarding, choose an
agent, and authorize that organization's Durin MCP. Setup verifies the saved MCP
bridge before recommending that you open the selected harness. It never starts an
agent conversation. Agent selection is interactive; `--agent` and `-a` are rejected.

Requires Node 24.15 or later in the Node 24 release line and an unlocked OS
credential store. This repository contains the standalone CLI and the client contracts it consumes.

## Run from this repository

```sh
git clone git@github.com:accessdurin/cli.git
cd cli
pnpm install --frozen-lockfile
pnpm build
node dist/cli.js --help
node dist/cli.js
```

The examples below use `durin` for this executable. To install a local build, run
`pnpm pack --pack-destination /tmp/durin-cli-release`, then install the resulting
archive with `npm install --global /tmp/durin-cli-release/accessdurin-cli-1.0.0.tgz`.
After the first npm release, run `pnpx @accessdurin/cli` or install the executable
with `npm install --global @accessdurin/cli`. Publishing this Git repository alone
does not make the package available on npm.
The default app origin is `https://app.getdurin.com`. That deployment must have
AuthKit CLI authentication and MCP authorization configured before onboarding works.

## Onboarding

1. Choose **Log in** or **Create an account**. Authorize the displayed device code
   in the browser. The CLI waits for the provider's verified response.
2. Select a verified organization, or create one if you have no memberships.
3. Answer the organization's required questions in the CLI. Complete payment setup
   in the browser when requested; the CLI waits for payment and tenant readiness.
4. Choose Claude Code, Codex, Cursor, Windsurf, or OpenCode. Install the harness first.
5. Authorize that organization's MCP in the browser. This consent is separate from
   your account login. The CLI checks the saved bridge and authenticated tool catalog.
6. Open your chosen harness when setup finishes. An empty catalog is reported as
   connected with no tools; ask an administrator to enable a connection or grant access.

`durin login`, `durin signup`, and `durin install` start or resume the same journey.
Resume reauthenticates your account and reloads server-owned onboarding progress.
It does not trust a saved completion flag. No pasted endpoint or token is required.

## Commands and flags

| Command                                                             | Purpose                                                                       |
| ------------------------------------------------------------------- | ----------------------------------------------------------------------------- |
| `durin`                                                             | Choose login/signup and complete onboarding                                   |
| `durin login` / `durin signup` / `durin install`                    | Start or resume interactive setup                                             |
| `durin verify`                                                      | Verify the selected profile's authenticated MCP catalog                       |
| `durin profiles list`                                               | List saved organization profiles                                              |
| `durin profiles use <id>`                                           | Select the terminal's active profile                                          |
| `durin tools list`                                                  | Print the authorized tool catalog as JSON                                     |
| `durin tools inspect <name>`                                        | Print a tool's native schema                                                  |
| `durin tools call <name> --input <file\|-> --idempotency-key <key>` | Invoke a governed tool                                                        |
| `durin mcp serve`                                                   | Start a noninteractive stdio bridge                                           |
| `durin mcp endpoint`                                                | Print the personal endpoint for a direct MCP client                           |
| `durin logout`                                                      | Revoke the selected MCP grant where supported and clear its local credentials |

| Flag                      | Applies to                                                               |
| ------------------------- | ------------------------------------------------------------------------ |
| `--profile <id>`          | `verify`, `logout`, `tools …`, `mcp …`; otherwise use the active profile |
| `--config-dir <path>`     | All commands; default `~/.config/durin`                                  |
| `--input <file\|->`       | `tools call`; a JSON object, at most 1 MiB; `-` reads stdin              |
| `--idempotency-key <key>` | Required for `tools call`; retain it on retries                          |
| `--approval-id <id>`      | `tools call`; resume the exact independently approved operation          |
| `--app-url <origin>`      | Interactive setup; use another HTTPS Durin deployment                    |
| `--dev`                   | Interactive setup; additionally allow guarded loopback HTTP              |
| `-h`, `--help`            | Show usage                                                               |

Unknown, misplaced, and extra arguments fail instead of being silently ignored.
Interactive setup requires a terminal. Scripts should always select `--profile`.

```sh
durin tools list --profile <profile-id>
durin tools inspect sandbox__read_document --profile <profile-id>
printf '%s' '{"resource":"engineering/handbook"}' | durin tools call sandbox__read_document \
  --profile <profile-id> --input - --idempotency-key handbook-read-1
```

Tool results retain their native content, structured output and errors. A tool error
produces exit status 1. After an approval-required response, obtain independent
approval in Durin, then repeat the same arguments and idempotency key with
`--approval-id`. An uncertain response is never retried automatically with a new key.

## Agent configuration

All adapters configure the CLI's stdio bridge. Entries are named
`durin-<first 12 characters of profile ID>` and pin the complete organization profile.
Changing the active terminal profile does not redirect an existing harness connection.

| Agent       | Default user configuration                              | Bridge shape                                    |
| ----------- | ------------------------------------------------------- | ----------------------------------------------- |
| Claude Code | `~/.claude.json`                                        | `mcpServers`, `type: stdio`, `command` + `args` |
| Codex       | `~/.codex/config.toml`                                  | `mcp_servers`, `command` + `args`               |
| Cursor      | `~/.cursor/mcp.json`                                    | `mcpServers`, `command` + `args`                |
| Windsurf    | `~/.codeium/windsurf/mcp_config.json`                   | `mcpServers`, `command` + `args`                |
| OpenCode    | `~/.config/opencode/opencode.json` or existing `.jsonc` | `mcp`, `type: local`, `command` array           |

Claude honors `CLAUDE_CONFIG_DIR`; Codex honors `CODEX_HOME`; OpenCode honors
`OPENCODE_CONFIG` and `XDG_CONFIG_HOME`. Unrelated settings and comments are preserved.
Malformed, disabled, conflicting, or concurrently modified configurations are refused.
Project or managed harness settings may override these user-level files.

The CLI copies its executable and native credential-store dependency to a stable
location under the profile directory. Agent configs contain absolute Node/runtime
paths and profile references, never tokens. Keep the referenced Node installation
available. `NODE_EXTRA_CA_CERTS` is passed to the verification child when present;
custom enterprise certificate settings must also be available to your harness.

## Direct MCP and credentials

`durin mcp endpoint --profile <id>` prints the resource URL. A direct HTTPS MCP
client authorizes that resource independently using discovered OAuth metadata and
PKCE. It does not inherit the CLI grant. Direct native tool calls supply Durin's
`_meta["io.durin/control"]` idempotency/approval metadata; see the
[wire example](#direct-tool-call-example). Clients that cannot
supply this metadata can use `durin mcp serve`.

The stdio bridge adds missing operation keys, keeps pending/uncertain calls across
process restarts, and resumes the same approval. It retains native schemas and
results. Its ledger stores hashes and operation IDs, not tool arguments. Successful
responses clear pending records; a later deliberate call may create a new operation.

MCP credentials use macOS Keychain, Windows Credential Manager, or Linux Secret
Service. There is no plaintext fallback. Account-session tokens remain in memory
only during onboarding. `logout` affects the selected CLI-owned MCP grant, retains
config/profile metadata, and reports remote revocation separately. Other clients'
OAuth grants and browser sessions are unaffected.

If a process dies while holding a profile lock, the CLI reports its exact lock path.
Stop Durin and the profile's bridges before removing that abandoned lock, then retry
with the same operation key. Locks are not stolen automatically.

## Direct tool call example

A direct MCP client supplies operation metadata with its tool request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "sandbox__read_document",
    "arguments": { "resource": "engineering/handbook" },
    "_meta": {
      "io.durin/control": {
        "idempotencyKey": "handbook-read-1"
      }
    }
  }
}
```

For an independently approved retry, preserve the arguments and idempotency key
and add `approvalId` inside the same control object. Use the CLI's stdio bridge
when your client cannot supply this metadata.

## Development and verification

```sh
pnpm install --frozen-lockfile
pnpm verify
# Optional actual OS credential-store round trip with synthetic values and cleanup:
DURIN_TEST_KEYRING=1 pnpm exec vitest run src/credentials.native.test.ts
pnpm pack --pack-destination /tmp/durin-cli-release
```

`pnpm verify` checks formatting, lint, strict types, CLI unit/integration tests,
the Node build, and repository credential patterns. Tests use isolated local
files and synthetic credentials. The actual OS credential-store test is opt-in.
No Durin server, website, infrastructure, provider credentials, or account sessions
are stored in this repository. Local tests do not prove hosted provider readiness
or acceptance by every installed agent harness.

`src/contracts` contains only the wire schemas this client consumes. `src/ports`
defines the CLI's narrow interfaces; adapters remain in `src`. The CLI has no
workspace dependency and does not require the private Durin monorepo to build.
The original source was extracted from Durin commit `a8b187a`; contract updates
must retain compatibility with the service's public responses.

## Credentials and publication

Never commit local environment files, API keys, account sessions, client profiles,
private keys, or credential-store exports. These are not required to build or test.
The secret check reports file names and credential categories, never their values.
For a broader offline scan, run `trufflehog filesystem --no-verification --no-update .`
on a clean source checkout and review all findings before publishing.

## Publish to npm

Publish from this standalone repository using Node 24.15 or later in the Node 24
release line and pnpm 11.21.0. The monorepo workspace package remains private.
You need access to the `@accessdurin` npm scope, either as its account owner or
through the `accessdurin` npm organization. Browser login does not authenticate
your terminal; `npm whoami` checks the terminal's session.

```sh
git clone git@github.com:accessdurin/cli.git
cd cli
pnpm install --frozen-lockfile
pnpm verify
npm whoami --registry=https://registry.npmjs.org/
# If whoami fails, sign in and complete the browser prompt:
npm login --registry=https://registry.npmjs.org/
npm publish --dry-run --access public --ignore-scripts=false
npm publish --access public --ignore-scripts=false
```

Complete npm's authentication/2FA prompt when requested. The package's
`publishConfig` selects the public npm registry and public access; this does not
change the GitHub repository's visibility. `prepublishOnly` runs `pnpm verify`,
and `prepack` builds the executable. The commands explicitly enable those hooks
even when npm's `ignore-scripts` setting is enabled. The package contains only `dist/cli.js`,
`package.json`, this README, and the MIT `LICENSE`.

After publishing, check the registry and run the released executable:

```sh
npm view @accessdurin/cli version --registry=https://registry.npmjs.org/
pnpx @accessdurin/cli --help
```

For subsequent releases, update the version with `npm version patch` (or `minor`
or `major`), push the version commit/tag, and publish again. npm will not accept
another release of the same name and version.

See npm's [scoped publishing guide](https://docs.npmjs.com/creating-and-publishing-scoped-public-packages/)
and [`npm publish` reference](https://docs.npmjs.com/cli/v11/commands/npm-publish/).

## License

MIT. See [LICENSE](LICENSE).
