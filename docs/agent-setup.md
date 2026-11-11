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
`src/skill-bundle.ts`. Setup does not fetch instructions from GitHub. The same
snapshot stays available in the stable runtime copied out of an `npx` cache.
Maintainers update it from a reviewed, committed checkout with
`pnpm skills:sync /path/to/skills`, then format, review, and verify the generated
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
| Claude Code        | `~/.claude.json`                      | `~/.claude/skills`            | `~/.claude/settings.json`, `PreToolUse`                                                          |
| Codex              | `~/.codex/config.toml`                | `~/.agents/skills`            | `~/.codex/hooks.json`, `PreToolUse`                                                              |
| Cursor             | `~/.cursor/mcp.json`                  | `~/.cursor/skills`            | `~/.cursor/hooks.json`, `preToolUse`                                                             |
| Windsurf           | `~/.codeium/windsurf/mcp_config.json` | `~/.codeium/windsurf/skills`  | `~/.codeium/windsurf/hooks.json`, pre-tool events                                                |
| OpenCode           | `~/.config/opencode/opencode.json(c)` | `~/.config/opencode/skills`   | `~/.config/opencode/plugins/durin.js`, V2 `execute.before` and V1 1.18.29+ `tool.execute.before` |
| Gemini CLI         | `~/.gemini/settings.json`             | `~/.gemini/skills`            | Same settings file, `BeforeTool`                                                                 |
| GitHub Copilot CLI | `~/.copilot/mcp-config.json`          | `~/.copilot/skills`           | `~/.copilot/hooks/durin.json`, `preToolUse`                                                      |
| Custom harness     | Descriptor-selected JSONC/TOML file   | Descriptor-selected directory | Compatible native adapter or a generated command descriptor                                      |

Claude paths honor `CLAUDE_CONFIG_DIR`; Codex configuration honors `CODEX_HOME`.
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
[Windsurf hooks](https://docs.windsurf.com/de/windsurf/cascade/hooks),
[OpenCode plugin APIs](https://opencode.ai/docs/build/plugins/migrate-v1/),
[Gemini hooks](https://geminicli.com/docs/hooks/),
[Gemini skills](https://geminicli.com/docs/cli/skills/),
and [Copilot hooks](https://docs.github.com/en/copilot/reference/hooks-reference).

## Local checks and Endpoint

Setup offers an installed Durin Endpoint helper when it can locate a supported
helper, a custom local checker, or skipping local hook setup. Skipping preserves any existing local hooks; it does not disable them. It does not
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
Unrelated payload fields, including transcript paths, are dropped. Harnesses that
do not provide a call identifier receive an opaque generated ID; Windsurf events
without session IDs receive generated session IDs too, so those events cannot
establish reliable session or transcript correlation.

Exit 0 continues; exit 2 denies. Other exits, startup errors, invalid input over
1 MiB, and timeouts also return a hook failure with exit 2. The CLI does not print
the checker's raw stdout/stderr or tool input. Endpoint's default policy pack
records violations and can return success after flagging; using that helper does
not turn every violation into a denial. Native harness failure and timeout behavior
still applies. Cursor hooks request `failClosed`; Codex requires reviewing and
trusting the configured hook with `/hooks`.

These hooks do not independently send tool inputs to a new Durin API. They pass
inputs to the local checker. An enrolled Endpoint app can report its own violation
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
`{"durin":{"command":["/absolute/node","/absolute/durin/runtime/cli.mjs","hooks","run",...]}}`.
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
