---
name: setup
description: Set up Durin access for Claude Code, install the compatible CLI, and guide interactive account and organization MCP authorization.
---

Connect the user's chosen Durin organization to Claude Code through the CLI's
existing profile-pinned MCP bridge.

1. Check `node --version`, `claude --version`, and whether `durin --help` works.
   Durin requires Node >=24.15.0 <25 and an unlocked OS credential store. If the
   CLI is absent or predates the 1.0.1 setup fixes, read
   [the installation reference](../../references/cli-installation.md) and guide
   the user through it. `durin` has no version flag; do not invent one. Inspect
   the installed package metadata if its version is unknown.
2. Have the user run `durin` in their own interactive terminal, choosing login
   or signup, the intended organization, and **Claude Code**. The wizard refuses
   noninteractive stdin/stdout. Do not pipe answers into it or launch it through
   Claude's noninteractive shell. `durin login`, `durin signup`, and
   `durin install` also start or resume this journey; `--agent` is unsupported.
3. Let the user complete the browser device-code authorization, organization
   questions, any requested payment, and separate MCP consent. Do not request
   tokens, credentials, payment details, or device codes in the conversation.
   Use an alternate `--app-url` or `--config-dir` only when provided by the user
   or their administrator; keep custom directory selection on subsequent calls.
4. After the user finishes, run `durin profiles list` and bind the intended
   organization to its full `id`. Profiles contain organization/principal
   metadata and endpoint URLs; summarize only what identifies the chosen profile.
   If several profiles could match the request, clarify which organization to
   use. Do not assume the active terminal profile is the requested organization.
5. Run `durin verify --profile <profile-id>` and inspect the exit code and JSON.
   A verified empty catalog means authenticated with no available tools. Request
   an administrator's connection/grant setup rather than promising provider access.
6. Have the user restart Claude Code to load a newly saved MCP bridge. Explain
   that it is named `durin-` plus the first 12 profile ID characters. CLI catalog
   verification alone does not prove Claude has loaded that server. Use
   `/durin:verify` for a connection check after restart.

The CLI owns configuration writes, credential storage, and bridge verification.
Use it instead of constructing a generic `.mcp.json`, editing permissions, or
creating a second server. A bridge includes an explicit `--profile` and
`--config-dir`; changing `durin profiles use` does not change that bridge's
organization. Account login does not substitute for MCP consent.
