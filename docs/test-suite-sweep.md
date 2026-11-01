# CLI test-suite sweep

Baseline: `b8f777e`, measured on 1 November 2026.

The sweep reduces the suite from 879 tests in 38 files to 779 tests in 36 files.
The passing Linux run changes from 877 passed / 2 skipped to 777 passed / 2 skipped.
The existing skips are the opt-in native credential-store test and the root-only
ownership test when running as an ordinary user.

`AGENTS.md` records the same approach for future test changes: extend existing
coverage where possible, preserve unique value and isolation when consolidating,
and verify performance claims without weakening discovery or assertions.

## Scope and CI inventory

The repository has one GitHub Actions verification job on `ubuntu-latest`. It
installs the frozen lockfile and runs `pnpm verify`: formatting, lint, strict
TypeScript, every discovered Vitest suite, the production build and the secret
scan. Test discovery, dependencies and the CI workflow are unchanged.

The review covered all 38 original test files: URL/host/profile contracts,
account and MCP authorization, setup, logout and revocation, bridge transport and
call persistence, agent configuration and runtime installation, command processes,
terminal/browser boundaries and credential/directory/lock handling.

Distinct host spellings, roles, deployment environments, unsafe browser schemes,
callback state/issuer/PKCE checks, credential-store failures, profile binding,
pending-grant persistence and filesystem security cases remain covered. The
host-pinning matrix stays intact. Tests at different integration boundaries stay
separate where they prove different wiring or side effects. The real socket,
stdio, child-process and lock-contention checks remain.

## Consolidations and retained assertions

| Suite                        | Before | After | How coverage survives                                                                                                                                                                                                                                         |
| ---------------------------- | -----: | ----: | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `contracts/cli-url`          |    127 |    58 | For each of the same 23 loopback URL forms, one case now checks endpoint rejection/acceptance, all four host roles, deployment allowlisting, profile validation and development-only HTTP. Every assertion and input remains.                                 |
| `logout`                     |     46 |    34 | The ten identical failure setups each assert both grant retention and the unfinished marker. Successful revocation also checks marker-before-network ordering, cleanup and unrelated credentials.                                                             |
| `logout-acceptance`          |     11 |     5 | The main logout matrix retains 400/401/403/404 responses, metadata 404 and refused redirects, including the HTML portal bodies and readable-token assertions. Marker-write failure, in-flight logout, metadata 401/403 and dangling symlinks remain separate. |
| `setup-mcp`                  |     45 |    42 | The disk-marker sign-in checks exactly one clear. Kept-grant success/failure cases also check revocation order, authorization-code grant type, fresh registration/browser interaction and absence of old tokens in other requests.                            |
| `setup-mcp-acceptance`       |      3 |     1 | One real initial-sign-in/re-sign-in sequence retains revocation ordering, changed tokens, fresh browser interaction and block clearing.                                                                                                                       |
| `revocation-marker`          |     25 |    24 | The successful logout ordering case already checks the same disk marker, result and working bridge/tools after logout.                                                                                                                                        |
| `config`                     |     37 |    32 | Each agent fixture checks creation in an empty home, then existing-settings preservation, actual format, comments, round-trip and idempotence. The existing file is explicitly made `0644` before checking that the update produces `0600`.                   |
| `install-runtime`            |      9 |     8 | The same fixture checks identical-runtime reuse before deleting the source package and executing the installed runtime and its native dependency. Tamper and permission cases remain separate.                                                                |
| `mcp-bridge` plus acceptance |      8 |     7 | The live-stream case performs failed logout under the profile lock with a real disk marker, proves a notification arrived beforehand, then checks no later delivery, aborted transport, recovery guidance and no further use of the old grant.                |
| `cli` plus host-pin E2E      |     12 |    12 | All built-command and real-server/terminal assertions share one build in `cli.test.ts`. Independent public-interface subprocess cases run concurrently with Vitest's existing concurrency bound.                                                              |
| `host-pin-acceptance`        |     74 |    74 | The resource-metadata redirect case waits for its correlated protocol error and proves the MCP request occurred before checking that no hostile host was contacted.                                                                                           |

Two files disappear after their assertions move into the retained suites:
`host-pin-e2e.test.ts` and `mcp-bridge-acceptance.test.ts`.

The bridge fixture now records every emitted line independently of explicit test
reads. A mutation probe exposed that the old fixture's negative-delivery assertion
could miss an unread leaked notification. The consolidated regression detects it.

## Execution costs

- Built-command tests perform one Vite build instead of two. Together with the
  required production build, `pnpm verify` performs two builds instead of three.
- A 500 ms host-pinning sleep becomes a protocol-completion wait with additional
  assertions that the request ran and failed.
- The redundant stream acceptance test's two 200 ms sleeps disappear with the
  duplicated setup. The retained regression waits for transport cancellation.
- The quiet-stream test advances the actual interval callback by 1,000 ms with
  only interval timers faked; stream I/O and timeout guards remain real.
- Agent configuration, installed runtime, authorization and revocation fixtures
  perform less repeated setup and cleanup.

## Verification and measurement method

Both versions pass the complete `pnpm verify` command on Linux with Node 24.22.1,
pnpm 11.21.0 and Vitest 4.1.12. Testing uses isolated WSL Ubuntu copies on the Linux
filesystem, with LF checkout text and Git metadata for the secret scan. The
Windows baseline has existing POSIX permission, symlink and rename failures, so
Windows timings are not used for comparison.

Coverage uses `@vitest/coverage-v8@4.1.12` installed only in the disposable copies.
It adds no project dependency or coverage exclusion. Before/after file inventories
and covered statement/function/branch source locations are compared, in addition
to aggregate percentages. Coverage does not instrument the spawned CLI processes;
their existing process, terminal and network assertions remain in the suite.

Four isolated implementation mutations are rejected by the retained regressions:
omitting the logout marker, reusing a kept grant during sign-in, relaying a stream
message after logout and disabling quiet-stream marker polling. Mutated source is
restored immediately; production source in this repository is unchanged.

Performance measurements alternate baseline/candidate order, use separate warm
copies and exclude installation. `pnpm test` runs use the same JSON reporter on
both versions. Full `pnpm verify` runs use the repository's normal scripts.
These are local Linux measurements; hosted Actions scheduling, checkout,
dependency installation and runner hardware are outside the comparison.

## Runtime results

Wall-clock seconds, including the pnpm invocation. A warm-up for each version is
excluded. Default allocation exposed 16 CPUs; the diagnostic restricts subprocesses
to two CPUs with Linux CPU affinity. Sample order alternates between versions.

| Command / CPU allocation | Samples per version | Baseline median | Candidate median | Median reduction |
| ------------------------ | ------------------: | --------------: | ---------------: | ---------------: |
| `pnpm test`, default     |                   5 |        4.3364 s |         2.9721 s |            31.5% |
| `pnpm test`, two CPUs    |                   3 |       17.1403 s |        12.2786 s |            28.4% |
| `pnpm verify`, default   |                   3 |        9.9243 s |         9.5654 s |             3.6% |

Individual samples in pair order:

| Measurement                | Baseline seconds                       | Candidate seconds                      |
| -------------------------- | -------------------------------------- | -------------------------------------- |
| Tests, default             | 4.5200, 4.3364, 5.1275, 3.8055, 3.4422 | 5.0442, 3.8028, 2.9721, 2.8908, 2.8768 |
| Tests, two CPUs            | 17.1403, 17.3077, 16.8766              | 12.2786, 12.2469, 14.0325              |
| Full verification, default | 10.6754, 9.9243, 9.5392                | 11.4466, 9.5654, 8.9279                |

The full-verification ranges overlap, so its small median reduction is noisy.
The test-phase reduction is the clearer measured improvement. No before/after
hosted GitHub Actions run was performed; total hosted CI savings remain unmeasured.
The runtime table covers the test changes before this report was added.

## Coverage results

Three additional complete coverage runs per version produced identical totals.
The union of covered source locations across those runs also matches exactly:
50 source files, 2,167 covered statement/function/branch locations, zero lost
locations and zero newly excluded files.

| Metric     |               Baseline |              Candidate |
| ---------- | ---------------------: | ---------------------: |
| Lines      | 1,136 / 1,301 (87.31%) | 1,136 / 1,301 (87.31%) |
| Statements | 1,262 / 1,483 (85.09%) | 1,262 / 1,483 (85.09%) |
| Functions  |     394 / 466 (84.54%) |     394 / 466 (84.54%) |
| Branches   |     511 / 693 (73.73%) |     511 / 693 (73.73%) |

To reproduce functional verification, use the repository's pinned Node/pnpm
versions and run:

```sh
pnpm install --frozen-lockfile
pnpm verify
```

To collect comparable coverage in disposable copies of baseline and candidate:

```sh
pnpm add -D @vitest/coverage-v8@4.1.12
pnpm exec vitest run --coverage --coverage.reporter=json --coverage.reporter=json-summary
```

The local ignored directory `node_modules/.cache/test-sweep/results/` contains
the benchmark samples, complete baseline/candidate test reports, coverage union
comparison, mutation failures and verification logs from this sweep.

## Validation after merging Sentry support

Merged `main` at `eb90558` into the PR after Sentry reporting and CPU profiling
landed. The consolidated CLI suite retains the new isolated-runtime assertions
and runs terminal subprocesses with `NODE_ENV=test`. Runtime-installation fixtures
also retain the native profiler and transitive-dependency checks.

The complete Linux `pnpm verify` passes with 782 tests passed and the same two
existing skips: 784 tests in 37 files. The runtime and coverage measurements above
describe the original `b8f777e` baseline comparison before Sentry support landed.
