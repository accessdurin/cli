# CLI state and distribution audit

Reviewed 11 November 2026 against the standalone CLI source, command entry points,
contracts, adapters, tests, plugin documentation, and build/release scripts. This
review concerns client control flow and distribution; it does not audit the
hosted gateway, identity provider, native Endpoint app, or third-party harnesses.

The CLI needs some lifecycle state to handle authorization, approvals, interrupted
operations, and concurrent processes. Removing those records would weaken retry
identity and sign-out guarantees. The useful simplification is to keep authority
with the server, make bounded retries direct, and record installation success
only after writing the files it describes.

## Findings addressed

| Area                    | Finding                                                                                                                                                                                                                 | Change                                                                                                                                                                                      |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Organization onboarding | Setup continued from status read before `start`. Recursive dispatch could repeatedly prompt or submit completion without checking the overall deadline. Organization checks covered polling but not mutation responses. | Reload status after starting, validate organization and journey on every response, bound dispatch and polling with one deadline, and poll an unchanged step instead of submitting it again. |
| Account API             | A recursive `retry` boolean encoded whether authentication had already been refreshed.                                                                                                                                  | Send once, refresh after a 401, then send once more. A second 401 ends the request.                                                                                                         |
| MCP request correlation | Error responses left request IDs tracked. A later passthrough request using that ID could inherit the old tool call's metadata and clear its pending operation.                                                         | Drop correlation on an error response or a new request with that ID, retaining the durable operation until a successful tool result is delivered.                                           |
| Skill/hook ownership    | Skill manifests were written concurrently with content; hook ownership was written before its target configuration. Interrupted writes could advance ownership without installing the artifact.                         | Write each artifact before its ownership record. Validate skill destinations before starting changes. Retry of the same installation recognizes matching installed content.                 |
| OAuth callback          | Deferred result construction used mutable placeholder resolver functions.                                                                                                                                               | Use Node's `Promise.withResolvers`; retain callback binding, timeout, and one-use enforcement.                                                                                              |
| Setup verification      | The verification subprocess's explicit environment omitted the telemetry opt-out.                                                                                                                                       | Forward `DO_NOT_TRACK` and the test environment to the verification subprocess. Document disabling reports, traces, and profiling.                                                          |
| Distribution            | The root and plugin MIT licenses existed, but bundled code notices, copied skills, and the separately shipped Node executable needed attribution preserved through packaging and installation.                          | Emit dependency notices during builds, preserve CLI notices in copied runtimes, install the reviewed skill license, and include the exact Node version's full license in OS archives.       |

Production dependencies were already exact versions, without carets. Development
ranges and the pinned pnpm tool version remain unchanged. The lockfile is retained.
Public clone/package metadata uses HTTPS. Contribution and confidential reporting
instructions are available without private repository access.

## State inventory and decisions

| Boundary and implementation                                                                                                                                                                             | Authority / lifetime                                              | Decision                                                                                                                                                                                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Command routing: `cli`, `command-arguments`, `cli-options`, `tool-commands`, `profile-commands`, `mcp-command`                                                                                          | Parsed invocation; command/flag tables                            | Keep the direct dispatch tables. They select behavior and do not track a journey.                                                                                                                                                                                                     |
| Setup: `setup`, `setup-account`, `select-organization`, `onboard-organization`, `account-api`                                                                                                           | Verified account and server onboarding status                     | Keep sequential setup functions and server step dispatch. No local onboarding completion cache is needed. The in-memory selected account session is necessary for rotated tokens.                                                                                                     |
| Device/OAuth authentication: `device-auth`, `mcp-oauth`, `setup-mcp`, `oauth-callback`                                                                                                                  | Provider protocol, challenge expiry, SDK callbacks                | Keep polling interval/deadline, PKCE verifier, discovery binding, and one-use callback checks. These are protocol and replay controls. Account API refresh no longer needs a separate retry flag.                                                                                     |
| Profiles: `profiles`, `contracts/cli-profile`, `profile-client`                                                                                                                                         | Validated profile scope plus explicit active-profile file         | Keep persistent organization binding. Saved agent bridges carry full profile IDs and do not follow later terminal profile selection.                                                                                                                                                  |
| Tool operations: `bridge-calls`, `ports/cli-call-ledger`, `remote-mcp`                                                                                                                                  | Gateway results and durable operation keys                        | Keep the pending ledger and approval consumption rules. Request correlation is transient and ends at a response; the ledger survives uncertain outcomes and restarts.                                                                                                                 |
| Bridge transport: `mcp-bridge`                                                                                                                                                                          | SDK transport plus local lifecycle                                | Keep protocol negotiation request ID, close idempotence, ordered delivery, and sign-out observation. Observing a newly blocked running bridge closes its stream; a bridge starting blocked can explain refusals. Removing the observation flag would change that documented behavior. |
| Logout/concurrency: `logout`, `revocation-marker`, `profile-lock`                                                                                                                                       | Revocation result, credential store, atomic marker and lock files | Keep crash recovery markers and pending old grants. Their write/delete order prevents refresh during unfinished logout and losing grants before retry. Keep ownership-bound locks and explicit abandoned-lock recovery.                                                               |
| Runtime installation: `install-runtime`, `runtime-packages`                                                                                                                                             | Content digest and atomic launcher replacement                    | Keep immutable version directories and the active launcher. Existing runtimes are checked before reuse. Preserve licenses with the copied code.                                                                                                                                       |
| Skills/hooks/configuration: `install-skills`, `install-hooks`, `managed-files`, `hook-config`, `config`, `bridge-config`, `hook-runner`                                                                 | Actual files, digests, prior managed invocation                   | Keep ownership evidence to preserve user edits. A saved hook configuration is not evidence of hook execution. Commit ownership after the target artifact.                                                                                                                             |
| Local boundaries: `agents`, `agent-installed`, `custom-harness`, `hook-checker`, `hook-input`, `browser`, `terminal-ui`, `terminal-text`, `private-directory`, `credentials`, `safe-fetch`, `contracts` | Current environment, validated inputs, OS facilities              | No additional workflow cache is needed. Preserve executable/path validation, host checks, inert terminal text, credential-store boundaries, and fail-closed hook outcomes.                                                                                                            |
| Diagnostics: `telemetry`, `telemetry-privacy`                                                                                                                                                           | One command and best-effort reporting                             | Keep the action-started guard: rerunning a command after its own error could duplicate an external effect. Preserve scrubbing and opt-out propagation.                                                                                                                                |
| Build and release: Vite, `scripts`, OS installers, Claude plugin                                                                                                                                        | Manifest/lockfile, verified release inputs, install destinations  | Preserve pinned runtime/dependency inputs, checksum verification, and explicit activation. Build/install records must describe files actually saved.                                                                                                                                  |

## Remaining design boundaries

The bridge groups unresolved native calls by tool name and canonical arguments
within a profile. An identical call can therefore resume an earlier unresolved
operation. The ledger expires 24 hours after its last state change; expiry does
not prove the earlier operation never ran. Retain this documented behavior until
a protocol change can distinguish a fresh intent from a retry. CLI users can
choose explicit operation keys.

Skill and hook installation spans multiple files and is not one filesystem
transaction. A failure can leave partial artifacts, but must not record their
installation as complete. Re-running the same setup reconciles matching files;
conflicts and edited files still require review. Do not add a global `installed`
flag as a substitute for checking the actual targets.

Do not merge the logout marker, pending old-grant list, and call ledger into a
single generic state machine: they have different authorities, recovery rules,
and security purposes. Further removal would require changing user-visible
guarantees, rather than simplifying an implementation detail.

## Validation

Regression checks cover post-start progress, stalled questions/completion,
organization changes at each response boundary, a second account 401, reused MCP
request IDs, interrupted artifact writes, preserved license files, and subprocess
opt-out inheritance. Existing OAuth, host, logout, filesystem, bridge, and harness
assertions remain in place. Run `pnpm verify` and `pnpm package:release`, then
inspect the npm archive, OS archive, and Debian package's license inventory.
The local Linux verification passes with 820 tests passed and two existing skips
(822 total). The baseline had 806 passed and the same two skips (808 total);
the additional cases cover the findings above. No test discovery or coverage
scope was narrowed, and no runtime improvement is claimed. Linux release packaging
and the packaged launcher also pass; archive inspection checks the CLI, dependency,
native package, and exact upstream Node license files.
Native macOS/Windows package execution and hosted authentication still require
their own target-environment checks.
