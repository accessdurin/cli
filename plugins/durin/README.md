# Durin for Claude Code

Connect Claude Code to your organization's Durin tools. Durin checks access and
policy, requests independent approval when required, and records governed tool
activity. This plugin teaches Claude how to set up the Durin CLI, verify an
organization profile, discover authorized tools, and preserve operation identity
when a call needs approval or returns an uncertain result.

## Install the plugin

In Claude Code, run:

```text
/plugin marketplace add accessdurin/cli
/plugin install durin@accessdurin
```

Reload plugins or restart Claude Code when prompted. The marketplace is hosted
by Durin; installing from it does not mean Anthropic has reviewed or listed the
plugin. The plugin is designed for Claude Code's local terminal and MCP support.

## Connect your organization

Run `/durin:setup`. You need Claude Code, Node 24.15 or later in the Node 24
release line, and an unlocked OS credential store. Install the CLI from this
public repository using the [installation instructions](references/cli-installation.md).
CLI 1.0.1 contains fixes for current MCP setup; npm 1.0.0 lacks those fixes.
Use the source installation until CLI 1.0.1 is published.

Complete `durin` in your own interactive terminal. Choose login or signup, your
organization, and **Claude Code**, then complete browser authorization. Account
login and MCP consent are separate. Onboarding can request organization and
payment details; complete those steps yourself. A Durin deployment must have
account authentication, MCP authorization, and tenant provisioning configured.

The CLI writes a profile-pinned MCP bridge to your Claude Code user configuration
and verifies its authenticated tool catalog. Restart Claude Code to load a newly
configured bridge. The plugin does not declare a second MCP server, so selecting
another active CLI profile cannot redirect the existing bridge.

## Skills

| Command                      | Purpose                                                              |
| ---------------------------- | -------------------------------------------------------------------- |
| `/durin:setup`               | Guide interactive CLI onboarding and connect Claude Code             |
| `/durin:verify [profile-id]` | Check a specific organization's access and diagnose connection gaps  |
| `/durin:tools [task]`        | Discover and use authorized tools, retaining approval and retry keys |

After setup, try "Use Durin to show the tools available to my organization" or
"Use Durin to perform this task with the tools my organization allows."
Available integrations depend on your organization's connections and grants;
the plugin does not enable providers or grant access.

## What runs and where data goes

The installed plugin contains Markdown instructions and JSON metadata. It has no
hooks, background processes, bundled executable, telemetry, or automatic package
installer. Claude can run CLI commands when needed for your request, subject to
Claude Code's normal permissions. Installation from source fetches code and
locked build dependencies from GitHub and the package registry.

The separately installed CLI contacts your selected Durin deployment (by default
`https://app.getdurin.com`), its discovered identity and MCP authorization services,
and its organization MCP gateway. Tool arguments go to that gateway and, on
execution, the selected provider. Catalogs, schemas, profile identifiers, and
tool results used by Claude become part of your Claude conversation. Share only
data appropriate for that conversation. Provider access, approval, audit, and
server retention are governed by your organization's Durin configuration and
service terms; the plugin does not set a server retention period.

The CLI saves organization/profile metadata and a stable runtime under
`~/.config/durin` (or your explicit `--config-dir`). MCP credentials live in
macOS Keychain, Windows Credential Manager, or Linux Secret Service, with no
plaintext fallback. Account tokens stay in memory during onboarding. CLI calls
require explicit idempotency keys; the MCP bridge retains hashes, operation keys,
and pending approval identifiers across restarts, without storing tool arguments
in its ledger. Never paste credentials into Claude or commit local profile data.

## Disconnect and uninstall

To clear a CLI-owned grant, run `durin logout --profile <profile-id>` in your
terminal. Review its output: local cleanup and remote revocation are reported
separately. Profile metadata and Claude's MCP entry remain; other clients' grants
are unaffected. Review and remove only the relevant `durin-…` entry in Claude
Code's MCP settings if you also want the bridge removed.

Run `/plugin uninstall durin@accessdurin` to remove the skills. Uninstalling this
plugin does not revoke the separate CLI grant or remove the configured bridge.
See [GitHub issues](https://github.com/accessdurin/cli/issues) for support.

## License

MIT. See [LICENSE](LICENSE).
