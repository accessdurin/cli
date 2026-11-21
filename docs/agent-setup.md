# Agent setup, skills, and local hooks

Run `npx @accessdurin/cli` in an interactive terminal. Setup waits for your account
choices, browser sign-in, organization onboarding, required payment setup, agent
selection, and separate organization MCP consent. It preserves existing settings
and verifies the saved MCP bridge before reporting completion.

You can give your coding agent this prompt:

> Set up Durin for my current Agent using npx @accessdurin/cli in an interactive terminal.
>
> Guide me through login or signup and organization onboarding, then select my Agent in the CLI. Complete the MCP setup for that organization. Pause for my account choices, browser sign-in, any required payment setup, and MCP consent. Preserve my existing settings.
>
> Run npx @accessdurin/cli verify, report the result, and tell me to reopen my Agent. If an interactive terminal is unavailable, give me the command to run myself.

After setup, use `npx @accessdurin/cli verify --profile <profile-id>` to check the
chosen organization explicitly. Reopen the agent to load the bridge and skills.
An empty authenticated catalog is a successful connection with no enabled tools;
an administrator must enable connections or grant access.

## Installed skills

The canonical skills live at [accessdurin/skills](https://github.com/accessdurin/skills):

| Skill            | When to use it                                              |
| ---------------- | ----------------------------------------------------------- |
| `durin-setup`    | Interactive account, organization, and agent onboarding     |
| `durin-tools`    | Discover tools and perform an authorized task through Durin |
| `durin-verify`   | Check MCP access and diagnose connection gaps               |
| `durin-profiles` | Choose organizations and disconnect a CLI grant             |
| `durin-hooks`    | Configure or diagnose a local checker and its hooks         |

The CLI embeds a snapshot with its source commit and SHA-256 digests in
`internal/app/skill_bundle.go`. Setup does not fetch instructions from GitHub. The
same snapshot stays available in the stable runtime copied out of an `npx` cache.
Maintainers update it from a reviewed, committed checkout with
`go run ./tools/syncskills /path/to/skills`, then review and verify the generated
file. Publish that source commit before releasing a CLI that references it.

Setup installs skills only for the selected harness. Each installed skill has a
`.durin-install.json` recording its source and digest. Identical files are reused;
updates replace only the previous managed content. Setup refuses to overwrite
an unrelated or user-edited skill. Review or move that directory aside to accept
a new version. Existing skill directories and other agent settings remain intact.

## Harness support

Paths below are user-level defaults. Every adapter configures a stdio MCP bridge
pinned to the complete organization profile. A terminal profile change does not
redirect existing agent connections.

| Harness            | MCP configuration                     | Skills                        | Local hook configuration                                                                         |
| ------------------ | ------------------------------------- | ----------------------------- | ------------------------------------------------------------------------------------------------ |
| Claude Code        | `~/.claude.json`                      | `~/.claude/skills`            | `~/.claude/settings.json`; check on `PreToolUse`                                                 |
| Codex              | `~/.codex/config.toml`                | `~/.agents/skills`            | `~/.codex/hooks.json`; check on `PreToolUse`                                                     |
| Cursor             | `~/.cursor/mcp.json`                  | `~/.cursor/skills`            | `~/.cursor/hooks.json`; check on `preToolUse`                                                    |
| Windsurf           | `~/.codeium/windsurf/mcp_config.json` | `~/.codeium/windsurf/skills`  | `~/.codeium/windsurf/hooks.json`; check on the pre-tool events                                   |
| OpenCode           | `~/.config/opencode/opencode.json(c)` | `~/.config/opencode/skills`   | `~/.config/opencode/plugins/durin.js`, V2 `execute.before` and V1 1.18.29+ `tool.execute.before` |
| Gemini CLI         | `~/.gemini/settings.json`             | `~/.gemini/skills`            | Same settings file; check on `BeforeTool`                                                        |
| GitHub Copilot CLI | `~/.copilot/mcp-config.json`          | `~/.copilot/skills`           | `~/.copilot/hooks/durin.json`; check on `preToolUse`                                             |
| pi                 | `~/.pi/agent/mcp.json`                | `~/.pi/agent/skills`          | `~/.pi/agent/extensions/durin.ts`; check on `tool_call`                                          |
| Kiro               | `~/.kiro/settings/mcp.json`           | not installed                 | `~/.kiro/hooks/durin.json` (v1 hook format); check on `PreToolUse`                               |
| Custom harness     | Descriptor-selected JSONC/TOML file   | Descriptor-selected directory | Compatible native adapter or a generated command descriptor                                      |

Besides the pre-tool check, durin registers a capture-only handler for every
other hook event each harness supports; see [hooks and the observer](hooks-and-observer.md)
for the event lists, the events deliberately left out, and what is captured.

Claude paths honor `CLAUDE_CONFIG_DIR`; Codex configuration honors `CODEX_HOME`. pi
honors `PI_CODING_AGENT_DIR`.
Codex's user skills use its current `~/.agents/skills` discovery path. Copilot
honors `COPILOT_HOME`. OpenCode honors `OPENCODE_CONFIG` for MCP configuration
and `XDG_CONFIG_HOME` for global skill/plugin discovery.

These are configuration adapters, not a claim that every agent version, desktop
surface, managed policy, or hosted/cloud session executes local hooks. OpenCode's
adapter provides separate V2 and V1 1.18.29+ callbacks. Older V1 versions require
a compatible custom adapter.
Cline, Roo Code, Continue, Amp, and other harnesses can use the custom descriptor
rather than guessing IDE-profile-specific storage locations. Their skill loader
and synchronous hook integration must honor the described contracts.

Sources for the native formats: [Claude skills](https://code.claude.com/docs/en/skills),
[Claude hooks](https://code.claude.com/docs/en/hooks),
[Codex skills](https://learn.chatgpt.com/docs/build-skills),
[Codex hooks](https://learn.chatgpt.com/docs/hooks),
[Cursor skills](https://cursor.com/docs/skills),
[Cursor hooks](https://cursor.com/docs/hooks),
[Windsurf skills](https://docs.windsurf.com/de/windsurf/cascade/skills),
[Windsurf hooks](https://docs.windsurf.com/de/windsurf/cascade/hooks) (common input fields, including `trajectory_id` and `execution_id`),
[OpenCode plugin APIs](https://opencode.ai/docs/build/plugins/migrate-v1/),
[Gemini hooks](https://geminicli.com/docs/hooks/),
[Gemini skills](https://geminicli.com/docs/cli/skills/),
and [Copilot hooks](https://docs.github.com/en/copilot/reference/hooks-reference).

## Local checks and Endpoint

Setup offers an installed Durin Endpoint helper when it can locate a supported
helper, a custom local checker, or skipping local pre-tool checks. Skipping keeps
a checker an earlier run configured and still installs capture-only hooks for
built-in harnesses (custom descriptors get no hooks without a checker). It does not
install the native Endpoint application or enroll a device for organization
reporting. MCP access works independently of local checker installation.

Endpoint's current helper protocol supports Claude Code and Codex. macOS helper
discovery checks the installed app. A helper installed elsewhere can be selected
through a checker descriptor. For Cursor, Windsurf, OpenCode, Gemini, Copilot, and
custom harnesses, supply a checker that understands the normalized input below;
do not label another harness's activity as Claude or Codex to satisfy an older
Endpoint helper. Extending the Endpoint detector/reporting protocol itself is a
separate change in that repository.

A checker descriptor is a JSON file selected during setup:

```json
{
  "command": "/absolute/path/to/local-checker",
  "args": [],
  "timeoutMs": 5000
}
```

On Windows use an absolute executable path, such as `C:\\Tools\\checker.exe`.
For a checker implemented in JavaScript, `command` is the absolute Node executable
and `args` contains the absolute script path. No shell interprets checker arguments.
The checker receives one JSON object on stdin:

```json
{
  "hook_event_name": "PreToolUse",
  "session_id": "session-id",
  "tool_use_id": "call-id",
  "tool_name": "mcp__server__tool",
  "tool_input": { "example": "native tool arguments" },
  "durin_harness": "cursor"
}
```

Optional normalized fields include `cwd`, `permission_mode`, `model`, and `turn_id`.
`durin_harness` is the installed harness ID (`claude`, `codex`, `cursor`, `windsurf`,
`opencode`, `gemini`, `copilot`, `pi`, `kiro`, or a custom ID). Unrelated payload fields, including
transcript paths, are dropped. Harnesses that do not provide a call identifier
receive an opaque generated ID for each call.

`session_id` is the harness's native session ID. Windsurf's is the Cascade
`trajectory_id`, and its per-turn `execution_id` becomes `turn_id`. If a Windsurf
event has no non-empty conversation ID, each call gets its own opaque ID, so calls
are never linked to an unrelated conversation.

Exit 0 continues; exit 2 denies. Other exits, startup errors, invalid input over
1 MiB, and timeouts also return a hook failure with exit 2. The CLI does not print
the checker's raw stdout/stderr or tool input. Endpoint's default policy pack
records violations and can return success after flagging; using that helper does
not turn every violation into a denial. Native harness failure and timeout behavior
still applies. Cursor hooks request `failClosed`; Codex requires reviewing and
trusting the configured hook with `/hooks`.

These pre-tool hooks pass inputs only to the local checker. Reporting local MCP
tool calls is a separate post-tool hook, described below. An enrolled Endpoint app can report its own violation
metadata according to its platform's capabilities and separate authorization.
Calls through the Durin MCP bridge still send arguments to the organization gateway
and provider. A local hook does not replace gateway permissions or approvals.

Run `durin hooks status` (or `npx @accessdurin/cli hooks status`) to inspect checker
availability and whether the saved configuration still matches. Execution remains
`not-verified` until tested in the actual harness. `durin verify` checks MCP access;
it does not verify hook execution or Endpoint enrollment.

Hook changes retain unrelated handlers and JSONC comments, use private temporary
files and backups named `*.durin-backup-*`, and reject concurrent modifications.
Disabled hooks are not silently enabled. A user-edited OpenCode plugin is not
overwritten. Backups can contain existing settings; keep them private.

## Local MCP tool calls

Agents also call MCP servers that run on your computer: a local command, or an HTTP
server on `localhost`/`127.0.0.0/8`/`::1`. Those calls do not pass through the Durin
gateway. Setup asks whether to report them to your organization (the default is
no) and, if you agree, adds a post-tool hook. Setup does not ask when the harness
has hooks turned off. Re-running setup after the CLI path changes replaces the
earlier handler instead of adding a second one.

| Harness     | Hook                                                               |
| ----------- | ------------------------------------------------------------------ |
| Claude Code | `PostToolUse` and `PostToolUseFailure`, matcher `mcp__.*`, `async` |
| Codex       | `PostToolUse`, matcher `mcp__.*`, `async`; review it in `/hooks`   |
| Cursor      | `afterMCPExecution`                                                |
| Windsurf    | `post_mcp_tool_use`                                                |

Gemini CLI, Copilot CLI, OpenCode, pi, Kiro and custom harnesses do not report yet.
The report handler is separate from the capture handler the observer uses for the
same events.

For each call, the hook finds the server the way the harness does, nearest scope
first: for Claude Code, servers added for the project (`projects[<folder>]` in
`~/.claude.json`), then the project's `.mcp.json`, then user servers; for Codex and
Cursor, the project's `.codex/config.toml` or `.cursor/mcp.json`, then the user file.
Project lookups start at the working directory and walk up its parent folders, so a
`cd` into a subfolder still finds the project. Claude Code rewrites characters outside
`[A-Za-z0-9_-]` in server names as `_` when it names tools, and a name may contain
`__`, so the hook compares names the same way and tries every split. It reports only
servers that run a local command or use a loopback URL. The Durin bridge
(`… mcp serve --profile …`), remote servers, disabled servers, servers it cannot
find (including Claude plugin servers) and built-in tools are skipped.

A report holds the harness, session, turn and call IDs, the server and tool names,
local or loopback transport, the outcome (success or failure; `unknown` for Windsurf,
whose payload does not say), and the tool arguments as JSON of at most 16 KiB.
Larger arguments are replaced with `{}` and marked truncated. Tool results are never
sent; hook events up to 16 MiB are read so a large result does not hide the call.
The CLI does not know your organization's capture policy, so it always sends the
arguments; the policy decides on Durin's side whether they are kept (metadata only, or
a redacted copy).

If Durin Endpoint covers the harness, Endpoint reports these calls and the CLI hook
stays quiet, so each call is reported once. Endpoint covers it when all three hold:
its helper is installed (found the same way setup finds it), it runs as that
harness's pre-tool hook (directly or as the CLI's local checker), and Endpoint is
signed in to an organization. The app shows that by refreshing
`~/Library/Application Support/Durin Observer/Organization/reporting.json` at least
hourly; the CLI treats a file older than two hours as signed out. Otherwise the CLI
hook reports. The marker does not name the organization, so the CLI cannot tell
whether Endpoint and the CLI profile use the same one.

The hook never blocks or fails a tool call and prints nothing. It writes nothing
while the profile is signed out: the profile is removed, a sign-out is unfinished,
or the OS credential store does not confirm the profile's MCP tokens within two
seconds (missing, unreadable or not answering). Otherwise it writes the call to
`reporting/spool/` under the Durin configuration directory (private files, at most
200 calls, kept up to 7 days) and starts `durin hooks flush` in the background. The
flush sends waiting calls with `durin_report_local_tool_call`, opening a connection
for at most 5 calls at a time (up to 50 per run), so the Durin bridge, which shares
the profile lock, waits at most for one short batch. It picks up calls queued while
it runs. A call the gateway rejects is retried up to 5 times. After a failure
(offline, signed out or rejected), flushes back off from one minute up to an hour.
Unreadable files and leftovers of interrupted writes are removed. Run
`durin hooks flush [--profile <id>]` to send waiting calls now.

To stop reporting, remove the Durin `hooks report` handler from the harness hook
file, then delete `reporting/<harness>.json` under the Durin configuration
directory. Without that file the hook does nothing.

## Custom harness descriptor

Choose **Custom harness** in setup and provide an absolute path to a JSON file:

```json
{
  "id": "custom-example",
  "name": "My coding harness",
  "configPath": "/home/me/.my-agent/mcp.json",
  "format": "jsonc",
  "serverKey": "mcpServers",
  "bridge": "arguments",
  "bridgeTransport": {},
  "skillsDirectory": "/home/me/.my-agent/skills",
  "hookConfig": {
    "path": "/home/me/.my-agent/durin-hook.json",
    "format": "custom"
  }
}
```

Use an ID of `custom` or `custom-` followed by lowercase letters, digits, and
hyphens. All file/directory paths must be absolute for the current OS. `format`
is `jsonc` or `toml`; `serverKey` names the MCP server collection. `bridge` is
`arguments` for separate `command`/`args`, or `command-array` for an argv array.
`bridgeTransport` supplies additional transport fields, for example
`{"type":"stdio"}`. Credentials are never written into these entries.

For `hookConfig.format: "custom"`, the generated file contains
`{"durin":{"command":["/absolute/durin/runtime/durin","hooks","run",...]}}`
(`durin.exe` on Windows).
Wire that argv directly into your harness's synchronous pre-tool callback, send
the normalized JSON on stdin, and honor exit 2 before executing the tool. Custom
hooks do not become active simply because this descriptor exists. A compatible
harness can instead choose `claude`, `codex`, `cursor`, `windsurf`, `opencode`,
`gemini`, or `copilot` as the hook format.

To remove local hooks, remove only the matching Durin handler/plugin from the
harness first, then its `hooks/<harness-id>.json` file under the Durin config
directory. Keep unrelated hooks. Skills can be removed by deleting only their
specific `durin-*` directories after reviewing local edits. `durin logout` revokes
the selected MCP grant; it does not uninstall skills, remove local hooks, sign
out of Endpoint, or revoke another client's grant.
