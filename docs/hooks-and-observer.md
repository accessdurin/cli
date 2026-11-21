# Hooks and the observer

Setup registers a durin handler for every hook event each supported harness
exposes, and installs the **observer**: a per-user background service that
receives those events, notices connections to AI providers, and keeps an
encrypted, bounded capture on this machine.

Nothing the observer collects leaves the machine. Telemetry is unchanged.
Hook commands and the observer never initialize telemetry, so captured content
can never reach it.

## Handlers

Every handler runs the copied runtime:

```text
<config>/runtime/durin hooks run --harness <id> --config-dir <config> --event <name>
```

There are two kinds of handler:

- **The check event** is each harness's pre-tool event (`PreToolUse`,
  `preToolUse`, `BeforeTool`, Windsurf's four `pre_*` events, pi's `tool_call`).
  - With a local checker configured, it runs the checker exactly as
    `durin hooks run` always has. The exit codes, Cursor's
    `{"permission":"allow"}` and the fail-closed errors are all unchanged.
  - A missing installation record still fails closed, as before.
- **Every other event only captures.** The handler forwards the payload to the
  observer and lets the harness continue.
  - It prints the harness's "continue" answer. That is nothing for most
    harnesses, `{}` for Cursor, and `{"continue":true}` for Cursor's
    `beforeSubmitPrompt`.
  - It always exits 0, even if the configuration directory, the installation
    record or the observer is missing or broken.
  - Capture handlers have a 10-second harness timeout (3 seconds where Codex
    caps it). They never fail closed, and Windsurf shows no output for them.

How a hook sends to the observer:

- It hands its event to the observer without waiting for it to be processed.
- It gives up after 150 ms when the observer is down or stuck. In practice, a
  failed connect returns in microseconds.
- When the observer is down, hooks behave exactly as they did before it existed.
- Handlers installed before `--event` existed (no `--event` argument) still run
  the checker. They also forward their payload as a `PreToolUse` event.

Without a checker (setup's "Skip local pre-tool checks"):

- The check event also only captures, as long as durin can read the installation
  record. Its handler runs the same command as a checking one, so durin learns
  that no checker is configured only from `<config>/hooks/<harness>.json`.
- If that record is missing, unreadable or invalid, or the configuration directory
  is not private (owned by you, mode `0700`), the check event fails closed: it
  exits 2 and the harness blocks the tool call. durin cannot tell a checker-less
  installation from one whose checker it can no longer read. To recover, run
  `chmod 700` on the directory or run setup again.
- Cursor's `preToolUse` is not registered at all, because a passive handler cannot
  answer it without overriding Cursor's own approval.
- A later setup run that skips the question keeps the checker an earlier run
  configured.
- Custom harness descriptors describe only the pre-tool check, so they get no
  hooks without a checker.

What setup preserves:

- Configuration edits go through `internal/jsonc`, so unrelated hooks, settings,
  comments and formatting are kept.
- Every change leaves a `.durin-backup-<uuid>` copy of the previous file.
- A file that changed since it was read is not overwritten.
- `disableAllHooks`, Gemini's `hooksConfig.enabled: false` and Codex's
  `[features] hooks = false` stop setup.
- Handlers recorded at the last installation (`handlers` in
  `<config>/hooks/<harness>.json`) are replaced in place, so moving the runtime
  does not add duplicates.
- Re-running setup with nothing changed rewrites nothing.

### Events per harness

Each list below is every event the harness's current documentation names, minus
the exclusions in the last column.

| Harness | Hook file | Events registered | Left out, and why |
| --- | --- | --- | --- |
| Claude Code | `~/.claude/settings.json` | `PreToolUse` (check), `PermissionRequest`, `PermissionDenied`, `PostToolUse`, `PostToolUseFailure` (tool events, matcher `.*`), `SessionStart`, `Setup`, `UserPromptSubmit`, `UserPromptExpansion`, `PostToolBatch`, `Notification`, `SubagentStart`, `SubagentStop`, `TaskCreated`, `TaskCompleted`, `Stop`, `StopFailure`, `TeammateIdle`, `InstructionsLoaded`, `ConfigChange`, `CwdChanged`, `DirectoryAdded`, `FileChanged`, `PreCompact`, `PostCompact`, `PostModelSwitch`, `Elicitation`, `ElicitationResult`, `SessionEnd` | `WorktreeCreate` and `WorktreeRemove`: a handler replaces git worktree handling. `PreModelSwitch`: a timed-out hook blocks the switch. `MessageDisplay`: fires while text streams. |
| Codex | `~/.codex/hooks.json` | `PreToolUse` (check), `PermissionRequest`, `PostToolUse`, `PreCompact`, `PostCompact`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`, `PreModelRequest`, `PostModelRequest`, `SessionStart`, `Interrupt`, `SessionEnd` | None. Codex skips new or changed hooks until the user trusts them in `/hooks`. |
| Cursor | `~/.cursor/hooks.json` | `preToolUse` (check, only with a checker), `beforeSubmitPrompt`, `sessionStart`, `sessionEnd`, `postToolUse`, `postToolUseFailure`, `subagentStop`, `afterShellExecution`, `afterMCPExecution`, `afterFileEdit`, `preCompact`, `stop`, `afterAgentResponse`, `afterAgentThought`, `afterTabFileEdit`, `workspaceOpen` | `beforeShellExecution`, `beforeMCPExecution`, `beforeReadFile`, `beforeTabFileRead` and `subagentStart` are marked "requires response". A handler that does not answer can block the action, and answering would override Cursor's approval. |
| Windsurf | `~/.codeium/windsurf/hooks.json` | `pre_mcp_tool_use`, `pre_run_command`, `pre_read_code`, `pre_write_code` (check), `post_read_code`, `post_write_code`, `post_run_command`, `post_mcp_tool_use`, `pre_user_prompt`, `post_cascade_response`, `post_cascade_response_with_transcript` | `post_setup_worktree` is undocumented. |
| Gemini CLI | `~/.gemini/settings.json` | `BeforeTool` (check), `AfterTool` (matcher `.*`), `BeforeAgent`, `AfterAgent`, `BeforeModel`, `BeforeToolSelection`, `SessionStart`, `SessionEnd`, `Notification`, `PreCompress` | `AfterModel` fires for every streamed chunk. |
| GitHub Copilot CLI | `~/.copilot/hooks/durin.json` | `preToolUse` (check), `sessionStart`, `sessionEnd`, `userPromptSubmitted`, `userPromptTransformed`, `postToolUse`, `postToolUseFailure`, `agentStop`, `subagentStart`, `subagentStop`, `errorOccurred`, `preCompact`, `permissionRequest`, `notification` | None. |
| OpenCode | `~/.config/opencode/plugins/durin.js` | Pre-tool check (V2 `tool.hook('execute.before')`, V1 `tool.execute.before`), `tool.execute.after`, `chat.message`, and every bus event under its own type (V2 `ctx.event.subscribe`, V1 `event`) | `message.part.updated` streams message parts. |
| pi | `~/.pi/agent/extensions/durin.ts` | `tool_call` (check: `{block: true}` when the checker denies or fails), and without waiting: `resources_discover`, `mcp_servers_change`, `session_*`, `before_agent_start`, `agent_*`, `ui_prompt_*`, `turn_*`, `message_start`, `message_end`, `context`, `context_with_system`, `before_provider_headers`, `before_provider_request`, `after_provider_response`, `cache_warming_decision`, `tool_execution_start`, `tool_execution_end`, `tool_result`, `model_select`, `thinking_level_select`, `input`, `user_bash` | `project_trust` must answer. `message_update`, `provider_stream_event` and `tool_execution_update` stream. |
| Kiro | `~/.kiro/hooks/durin.json` (v1 format) | `PreToolUse` (check), `PostToolUse`, `SessionStart`, `Stop`, `UserPromptSubmit`, `PreTaskExec`, `PostTaskExec`, `PostFileCreate`, `PostFileSave`, `PostFileDelete` | Session End, Pre/Post Compaction and Permission Request are documented only by display name; their JSON spellings are unverified. |

Notes for each harness:

- **Claude Code**: plain stdout from `UserPromptSubmit`, `UserPromptExpansion`,
  `SessionStart`, `PostModelSwitch` and `PreCompact` reaches the model. Capture
  handlers therefore print nothing.
- **Codex**: Codex skips new or changed hooks until they are trusted (Codex
  `/hooks`). Plain stdout on `SessionStart`, `UserPromptSubmit` and
  `SubagentStart` is added to the context, so capture handlers print nothing.
- **Cursor**: only JSON fields reach the model, so `{}` is inert.
- **Windsurf**: `docs.windsurf.com` now redirects to Devin's Cascade docs, which
  describe a new Claude-style format. The legacy `hooks.json` events above could
  not be confirmed on a current official page; see the sources below.
- **Gemini CLI**: the official reference says Antigravity CLI replaced Gemini CLI
  on 2026-06-18.
- **GitHub Copilot CLI**: a non-zero exit fails `preToolUse` closed, so capture
  handlers always exit 0.
  - The current reference documents command handlers as `bash` / `powershell` /
    `command`. The pre-existing pre-tool handler, and therefore the new ones,
    use `exec` / `args`. **Verify against a real Copilot CLI before release.**
- **OpenCode**: capture calls are fire-and-forget. The pre-tool call is awaited
  only when a checker is configured.
- **pi**: event handlers return nothing, so they change nothing.
- **Kiro**: stdout on exit 0 is added to the agent's context, so handlers print
  nothing.
  - Kiro CLI V3 loads global hooks from `~/.kiro/hooks/`. Whether the Kiro IDE
    loads that directory is undocumented; the IDE certainly loads workspace
    `.kiro/hooks/`.
  - Kiro has no documented user skills directory, so setup installs no skills
    for Kiro.

Sources (read 2026-11-18):

- Claude Code: <https://code.claude.com/docs/en/hooks>
- Codex: <https://learn.chatgpt.com/docs/hooks> (redirected from developers.openai.com/codex/hooks)
- Cursor: <https://cursor.com/docs/agent/hooks>
- Windsurf:
  - <https://docs.devin.ai/desktop/cascade/hooks> (redirected from docs.windsurf.com/windsurf/cascade/hooks)
  - <https://docs.devin.ai/cli/extensibility/hooks/overview.md>
  - Legacy event names, third-party: <https://pkg.go.dev/github.com/plexusone/assistantkit/hooks/windsurf>
- Gemini CLI: <https://geminicli.com/docs/hooks/reference/>
- GitHub Copilot CLI: <https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-hooks-reference>
- OpenCode:
  - <https://opencode.ai/docs/plugins/>
  - <https://opencode.ai/docs/build/plugins/server>
  - <https://opencode.ai/docs/build/plugins/migrate-v1/>
  - <https://opencode.ai/v1/docs/plugins.md>
- pi, from <https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent/docs>:
  - `extensions.md`
  - `skills.md`
  - `configuration.md`
  - `mcp.md`
- Kiro:
  - <https://kiro.dev/docs/hooks/>
  - <https://kiro.dev/docs/hooks/types/>
  - <https://kiro.dev/docs/hooks/actions/>
  - <https://kiro.dev/docs/ide/whats-new-v1/hooks/>
  - <https://kiro.dev/docs/cli/v3/hooks-migration/>
  - <https://kiro.dev/docs/custom-agents/configuration-reference/>
  - <https://kiro.dev/docs/reference/settings/>
  - <https://kiro.dev/docs/mcp/configuration/>
- Providers:
  - <https://developers.openai.com/api/docs/guides/data-residency>
  - <https://platform.claude.com/docs/en/api/overview>
  - <https://openrouter.ai/docs/api/reference/overview>
  - <https://openrouter.ai/docs/guides/features/in-region-routing>

## The observer

`durin observe --config-dir <config>` is the observer. Setup installs it as a
per-user service; `durin observer install` does the same by itself.

| OS | Service | Restarts on crash |
| --- | --- | --- |
| Linux | `~/.config/systemd/user/durin-observer.service` (`systemctl --user`) | `Restart=always`, plus `MemoryMax=256M`, `Nice=10`, idle I/O class |
| macOS | `~/Library/LaunchAgents/com.getdurin.observer.plist` (`launchctl bootstrap gui/<uid>`) | `KeepAlive` with `SuccessfulExit=false` |
| Windows | Scheduled task "Durin Observer" at logon, least privilege, hidden | `RestartOnFailure`, and `observe --supervise` restarts its worker process |

How the service runs:

- It always runs `<config>/runtime/durin`, the copy setup publishes, never a path
  inside an npm or Homebrew cache.
- Re-running setup restarts it, so an updated runtime is picked up.
- Nothing asks for elevation.
- If the service manager is unavailable (no systemd user session, for example),
  setup reports it and continues; hooks keep working without the observer.

| Command | What it does |
| --- | --- |
| `durin observer status` | JSON: the service state, whether the observer answers, its counters (events, stored and dropped records, detections, open provider connections, RSS), the capture size, and every DNS source with its state. |
| `durin observer remove` | Stops and unregisters the service and deletes its definition. The capture stays until purged. |
| `durin observer purge` | Deletes the whole capture. A running observer closes its segment first. |
| `durin observer export` | Prints the decrypted capture as JSON lines, to inspect what was captured. |

### IPC

The observer listens on one local channel for every harness and session:

- **Linux and macOS**: a Unix socket, `<config>/observer/observer.sock`.
  - The directory is 0700 and the socket 0600.
  - Peer credentials (`SO_PEERCRED`, `LOCAL_PEERCRED`) must match the observer's
    user.
  - If that path is too long for a socket, it moves to
    `$TMPDIR/durin-<uid>/<hash>.sock`.
- **Windows**: a named pipe, `\\.\pipe\durin-observer-<hash>`.
  - Its DACL grants access only to the current user, and it rejects remote
    clients.
  - Clients refuse a pipe owned by another user.

The protocol has three frame types:

- Each frame is a 4-byte big-endian length followed by JSON.
- `event` frames carry a hook payload: harness, event, time, hook pid and parent
  pid. The observer acknowledges nothing.
- `status` and `purge` frames get one reply.

Bounds:

- At most 32 connections are read at once.
- Queued records are bounded to 32 MiB; past that, records are counted as
  dropped, never buffered without limit.
- The Go heap has a 96 MiB soft limit.

### Capture: what, where, how long

What is captured:

- **Hook events**: the full payload each harness sends. That includes prompts,
  tool inputs and outputs, file paths, and session and transcript metadata, so it
  **may contain secrets and source code**. Each record also has the harness,
  event name, time, hook pid and parent pid.
- **Provider connections**: open and close records with the provider, hostname,
  remote and local address, TCP state, pid, executable, harness when known, and
  duration.
- **DNS queries for provider names**: only from sources you enable. Each record
  has the source, provider, hostname, answer addresses and pid when the source
  gives one.

Where it is stored:

- Records are stored in `<config>/observer/capture/<nanoseconds>.seg`, mode 0600.
- Each record is sealed on its own with AES-256-GCM, using a random nonce and the
  segment name as additional data.
- The 256-bit key is the OS credential store entry `observer:capture-key` under
  the CLI's service (`com.getdurin.cli`), reached through `internal/credentials`.
- There is no plaintext fallback. While the credential store is locked or
  unavailable:
  - Nothing is written, and records are counted as dropped.
  - `observer status` says why.
  - The observer retries every minute.
- Segments sealed with another key (after the key entry was deleted) are skipped
  on export and purged by age and size like any other segment.

How long it is kept:

- The capture is bounded to **512 MiB** and **7 days**.
- When either bound is exceeded, segments are deleted oldest first.
- Segments rotate at 8 MiB (smaller with a smaller bound) or after one hour.
- Purging runs as segments rotate, while writing, and every 10 minutes.

To change the bounds, write `<config>/observer/config.json` and restart the
service (`durin observer install`):

```json
{
  "captureMaxBytes": 268435456,
  "captureMaxAgeHours": 48,
  "pollIntervalSeconds": 5,
  "dnsSources": ["systemd-resolved"]
}
```

To delete everything:

1. Run `durin observer purge`.
2. To stop capturing, also run `durin observer remove`. Without the service,
   hooks keep their pre-tool behaviour and send nothing anywhere.
3. To make old segments permanently unreadable, delete the `observer:capture-key`
   credential entry.

### AI-provider detection

Known providers are matched by exact hostname. There is no suffix matching; each
documented API subdomain is listed. The table is `observer.Providers`; add a row
to add a provider.

| Provider | Hostnames |
| --- | --- |
| OpenAI | `api.openai.com`, data-residency hosts `us`, `eu`, `au`, `jp`, `in`, `ae`, `ca`, `sg`, `kr`, `gb` `.api.openai.com` |
| Anthropic | `api.anthropic.com` |
| OpenRouter | `openrouter.ai`, `eu.openrouter.ai`, `us.openrouter.ai` |

**Open connections.** Every 5 seconds the observer reads the OS socket table
without administrator rights, so it sees the connections of this user's
processes. It keeps only established or connecting sockets whose remote address
is a cached resolution of a provider hostname.

| OS | Socket table | Process lookup |
| --- | --- | --- |
| Linux | `/proc/net/tcp{,6}`; `/proc/<pid>/fd` is scanned only for inodes that matched, and the result is cached | `/proc/<pid>/{stat,exe,cmdline}` |
| macOS | `lsof -nP -iTCP -FpcnT` | `sysctl kern.proc.pid` and `kern.procargs2` |
| Windows | `GetExtendedTcpTable` (IPv4 and IPv6, owner pid) | `QueryFullProcessImageName`, toolhelp snapshot |

On macOS, libproc would avoid the subprocess but needs cgo, which the
cross-compiled release builds do not use.

Caching:

- Provider hostnames are re-resolved every 5 minutes.
- A failed refresh keeps the previous answer for up to an hour, then purges it.
- Process lookups are cached for 10 minutes, at most 1024 entries, oldest evicted
  first.

How a connection is attributed to a harness:

- Hook events teach the observer which processes belong to which harness. The
  hook's parent is recorded, and its grandparent when the parent is a shell.
- Otherwise a harness is recognized from the executable or script of the process
  or one of up to 8 ancestors. This covers `claude`, `codex`, Cursor, Windsurf,
  `gemini`, `copilot`, `opencode`, `pi`, Kiro and `kiro-cli`, and node or bun
  running their npm packages.

**DNS lookups** (opt-in, best effort; list the sources in `dnsSources`):

| Source | OS | What it needs |
| --- | --- | --- |
| `systemd-resolved` | Linux | `resolvectl monitor --json=short`, which usually needs root |
| `unified-log` | macOS | `log stream` of mDNSResponder. Host names are `<private>` unless private logging is enabled, and then nothing matches. |
| `windows-dns-client` | Windows | The `Microsoft-Windows-DNS-Client/Operational` log, which an administrator must enable. It is polled with `wevtutil`. |

A source that cannot run is reported as `unavailable` with its error. It is
retried with backoff up to every 30 minutes. A source never prompts for
elevation. Answers a source sees are added to the resolution cache.

**Extension point.** Every detection calls each `observer.ProviderTrafficHook`
with the provider, host, source, phase, pid, executable, harness and timing. The
only hook today is a no-op.

The observer never intercepts, proxies, decrypts or modifies traffic, and
installs no root CA.

For tests and split-horizon DNS diagnosis, `DURIN_OBSERVER_RESOLVE=host=address,...`
pins provider hostnames to addresses. Only provider hostnames are accepted.

## Performance

These numbers are measured, not estimated, with
`DURIN_LOAD=1 DURIN_LOAD_SECONDS=60 e2e/run.sh -run TestObserverLoad -v`.

The test machine was a Linux x86-64 cloud container. The load was:

- 32 concurrent sessions;
- payloads of 1 KiB, with 5 % at 64 KiB and 0.1 % at 1 MiB;
- 50 rolling connections to two pinned provider addresses;
- a 1-second socket poll, five times the default rate.

| Measurement | Result |
| --- | --- |
| Idle | RSS 18.4 MiB, CPU 0.30 % of one core (10 s) |
| Sustained load | 927,128 events in 60 s (15,452/s), no send failures, no drops; CPU 149 %; peak RSS (VmHWM) **40.1 MiB** |
| After load | RSS 22.5 MiB, CPU 0.30 % |
| Capture | 507.9 MiB in 65 segments, held under the 512 MiB bound by purging |
| Detections | 1,180 open/close records |
| Enqueue latency, client side (dial + write) | p50 20 µs, p95 520 µs, p99 1.63 ms, max 15.9 ms |
| `durin hooks run --event PostToolUse`, start to exit, observer down | p50 12.97 ms, p95 16.94 ms, p99 20.36 ms |
| The same, observer up and idle | p50 12.72 ms, p95 18.66 ms, p99 22.71 ms |

Hook latency is process start-up. The observer adds no measurable time, whether
it is up or down.

## What still needs a real macOS or Windows run

Linux is exercised end to end by the e2e suite. macOS and Windows code
cross-compiles and has unit tests for its pure parts: definitions, quoting, and
the socket-table and DNS parsers.

Still to be verified on real macOS:

- `launchctl bootstrap`, `bootout` and `print`;
- `lsof` attribution and the `kern.procargs2` process lookup;
- peer credentials;
- the unified-log source.

Still to be verified on real Windows:

- `schtasks /XML` with the UTF-16 definition, and the restart behaviour;
- the named pipe ACL and owner check;
- `GetExtendedTcpTable` on a live system;
- `QueryFullProcessImageName`;
- the `wevtutil` source;
- `observe --supervise`.

On every OS, each harness's actual execution of the new events, beyond writing
the configuration, still needs a run with that harness (`hooks status` reports
`execution: not-verified`).
