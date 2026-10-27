# Submit the Durin Claude Code plugin

This repository contains a Claude Code plugin bundle at `plugins/durin` and an
optional Durin-owned marketplace at `.claude-plugin/marketplace.json`. Submit the
plugin folder, not the marketplace root. The plugin version is independent of
the CLI package version. Creating or installing this marketplace does not submit
the plugin to Anthropic or establish a directory listing.

## Source and listing

| Portal field               | Value                                                        |
| -------------------------- | ------------------------------------------------------------ |
| Submission type            | Plugin bundle                                                |
| Repository                 | `https://github.com/accessdurin/cli`                         |
| Plugin path                | `plugins/durin`                                              |
| Tracked branch after merge | `main`                                                       |
| Plugin identifier          | `durin`                                                      |
| Plugin version             | `1.0.1`                                                      |
| Listing icon               | `plugins/durin/.claude-plugin/icon.png`; 1024 × 1024 PNG     |
| Publisher                  | Durin                                                        |
| Supported surface          | Claude Code                                                  |
| Documentation              | `https://github.com/accessdurin/cli/tree/main/plugins/durin` |
| Support                    | `https://github.com/accessdurin/cli/issues`                  |
| Privacy                    | `https://getdurin.com/privacy`                               |
| License                    | MIT; regular `LICENSE` file included in the plugin folder    |

The README and manifest supply the listing text. The maintainer must provide a
real monitored contact email in the portal; this repository does not invent one.
Submit from the Claude organization that should own the listing long term and
connect a GitHub account with push access. Anthropic requires a paid Claude plan
and the applicable Owner/Directory role on managed organizations.

## Validate the release

Run with the Node version in `.node-version` and pnpm from `packageManager`:

```sh
pnpm install --frozen-lockfile
pnpm verify
claude plugin validate --strict ./plugins/durin
claude plugin validate --strict .
```

The plugin contains regular text files and a PNG icon and uses default `skills/` discovery.
Its resources, README, and license are inside the plugin folder. It contains no
hooks, MCP declaration, executable, package install hook, environment credential
reference, or symlink. Onboarding delegates MCP registration and credential
handling to the separately installed CLI, preserving its profile binding.
CLI 1.0.1 is available in source; npm currently offers only 1.0.0. Before public
launch, publish/verify the compatible CLI release and update the bundled
installation reference, or explicitly retain the tested source installation.

In a fresh Claude Code session, test local loading:

```sh
claude --plugin-dir ./plugins/durin
```

Confirm `/durin:setup`, `/durin:verify`, and `/durin:tools` appear. Exercise setup
with a test organization, verify its profile-pinned MCP entry after restart, and
compare a real authorized tool task with and without the plugin. Test empty
catalog, denied policy, independent approval, and uncertain result handling.
The offline CLI suite and manifest validation do not prove hosted authentication,
provider access, skill behavior in a model session, or portal acceptance.

### Listing metadata and icon

The manifest's `icon` points to `./.claude-plugin/icon.png`, relative to the plugin
root. This is the existing Durin brand mark exported as a 1024 × 1024 PNG under
2 MB. The portal accepts a square PNG or JPEG from 512 to 2048 pixels on each side;
SVG and WebP are not accepted as listing icons.

Include the icon before the first time the plugin is saved or submitted in the
developer portal. The portal captures the listing icon only then; adding or
changing the repository image afterward does not replace an existing listing icon.

Keep `documentationUrl`, `supportUrl`, and `privacyPolicyUrl` in `plugin.json`. The directory reads
them for the listing, while Claude Code ignores them at load time. An `UNKNOWN_KEY`
notice for these fields is informational and does not require removing them.
Claude Code 2.1.281 and later accepts these directory fields in manifest validation;
older validators may warn about them, including `icon`.

## Data handling answers to review

Use the plugin README's data flow as evidence, and confirm service-specific facts
with the publisher before answering the portal:

- Claude sees selected profile metadata, catalogs/schemas, and relevant tool
  arguments/results during requested tasks; these can contain personal data.
- CLI authentication and tool execution use the selected Durin deployment,
  discovered authorization services, the organization's gateway, and providers.
  Source installation fetches GitHub code and registry dependencies. These are
  disclosed in the README, including services outside a declared connector.
- OS credential storage and the local profile/call ledger are owned by the CLI.
  The plugin adds no telemetry or separate persistent data store. Uninstalling
  it does not erase CLI profiles, grants, Claude conversations, or server data.
- Confirm Durin's actual server retention periods, applicable privacy policy,
  intended audience, and the portal's compliance acknowledgements; the plugin
  does not establish those policies.

## Submit and maintain

After merging the verified plugin, open
[the developer portal](https://claude.ai/directory/manage), choose **Submit new**
and **Plugin bundle**, enter the source fields above, and select **Validate**.
Fix blocking findings and revalidate the exact new commit. Review listing and
data handling details, provide the monitored contact address, acknowledge the
terms, then select **Submit for review**. A scan and Anthropic review still apply.
Do not mark the plugin submitted or approved until the portal confirms it.

For a published listing, increment the plugin manifest version with each release
and merge to the tracked branch. Update the CLI source revision or pinned npm
version in the installation reference when compatibility changes. Use the same
organization and existing submission to maintain the listing.

The plugin registers no remote MCP server. If Durin later submits its hosted
gateway as a connector, that is a separate submission and readiness review;
use the actual authorized endpoint and authentication configuration.

References:
[Publish to the directory](https://claude.com/docs/directory/publish),
[pre-submission checklist](https://claude.com/docs/plugins/pre-submission-checklist),
[submit a plugin](https://claude.com/docs/plugins/submit), and
[Claude Code plugin reference](https://code.claude.com/docs/en/plugins-reference).
