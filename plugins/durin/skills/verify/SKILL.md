---
name: verify
description: Verify a Durin organization profile and diagnose missing Claude Code tools, authorization, or credential-store problems.
---

Check the user's Durin connection without changing organization selection or
revoking a grant. Treat any supplied arguments as input, not shell commands.

1. Use `durin profiles list` to find the intended organization and full `id`.
   Preserve the user's explicit `--config-dir`, if any. Select the requested
   profile or the one already established for this task; clarify genuine
   ambiguity between organizations. Profile IDs are 64 lowercase hexadecimal
   characters. Do not rely on the active profile or paste complete profile files
   into the conversation.
2. Run `durin verify --profile <profile-id>` and
   `durin tools list --profile <profile-id>`. Verify the exit codes and parse
   their JSON output. Summarize whether authentication works and how many tools
   are visible. Discovery does not prove provider execution succeeds.
3. When checking Claude's own connection, run `claude mcp list` and look for
   `durin-<first-12-profile-id-characters>`. Inspect only that server's status;
   avoid repeating unrelated server configuration. If it is missing or has not
   loaded, guide `/durin:setup` or a Claude restart as appropriate. Do not claim
   Claude's bridge is healthy merely because the CLI's catalog check passed.

Use these outcomes to choose the next step:

| Observation                         | Next step                                                                                              |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------ |
| CLI absent or older setup fails     | Read [installation](../../references/cli-installation.md); install the compatible source build         |
| No matching saved profile           | Guide `/durin:setup` in the user's interactive terminal                                                |
| Authenticated, zero available tools | Ask the organization administrator to enable a connection and grant access                             |
| Keychain/credential store locked    | Have the user unlock their OS credential store; do not suggest plaintext tokens                        |
| Consent expired or revoked          | Have the user rerun `durin` in a terminal and complete this organization's MCP consent                 |
| CLI works, Claude entry fails       | Check restart, exact profile/config directory, retained Node runtime, and scoped configuration errors  |
| Conflicting or invalid config       | Explain the CLI error and have the user review the relevant entry; do not overwrite unrelated settings |
| Abandoned profile lock              | Report the exact lock path; require stopping that profile's CLI and bridges before manual removal      |

Do not switch organizations as a workaround, remove a lock while its process may
still run, or use `logout` to diagnose a read-only connection issue. Hosted
authentication or gateway failures must be reported as observed blockers.
