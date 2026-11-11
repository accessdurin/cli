# Install the compatible Durin CLI

The plugin uses the separately installed `durin` executable. It does not contain
or automatically install a CLI runtime. CLI 1.1.0 adds portable skill and hook
setup. Package channels may lag this checkout; use this revision's source until
1.1.0 is released. Do not substitute an unverified package launcher.

In your own terminal, with Node 24.22.1 or later in the Node 24 release line and
pnpm 11.21.0, build the reviewed source revision in a fresh directory:

```sh
git clone https://github.com/accessdurin/cli.git durin-cli
cd durin-cli
git rev-parse HEAD # Record and review the source revision.
pnpm install --frozen-lockfile
pnpm verify
pnpm pack --pack-destination .
npm install --global ./accessdurin-cli-1.1.0.tgz
durin --help
durin
```

Choose **Claude Code** during onboarding; `--agent` and `-a` are unsupported.
Complete login, organization questions, any requested payment step, and MCP
consent in your terminal/browser. Keep the Node installation available: the
configured bridge uses its absolute path. The CLI copies its executable and
credential-store dependency into a stable profile runtime, so it does not depend
on the source checkout remaining at that location.

If your organization uses a different deployment, use its administrator-provided
HTTPS origin with `durin --app-url <origin>`. Retain an explicit `--config-dir`
on later commands if you used a custom directory during setup.

The CLI sends scrubbed error reports and traces to Sentry; set `DO_NOT_TRACK=1` to turn this off.

After a publisher verifies that 1.1.0 exists on npm, the equivalent pinned
installation is `npm install --global @accessdurin/cli@1.1.0`. Publication of this
GitHub repository alone does not publish the npm package.
