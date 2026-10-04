# Install the compatible Durin CLI

The plugin uses the separately installed `durin` executable. It does not contain
or automatically install a CLI runtime. CLI 1.0.1 fixes current bootstrap,
OAuth discovery, and approval responses. npm currently offers 1.0.0; use the
public source below until 1.0.1 is released. Do not substitute an unpinned
package launcher for this installation.

In your own terminal, with Node 24.15 or later in the Node 24 release line and
pnpm 11.21.0, build the reviewed source revision in a fresh directory:

```sh
git clone https://github.com/accessdurin/cli.git durin-cli
cd durin-cli
git checkout --detach b224e2bfe357130b42470b1628b5bc7f8ec92d7d
pnpm install --frozen-lockfile
pnpm verify
pnpm pack --pack-destination .
npm install --global ./accessdurin-cli-1.0.1.tgz
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

After a publisher verifies that 1.0.1 exists on npm, the equivalent pinned
installation is `npm install --global @accessdurin/cli@1.0.1`. Publication of this
GitHub repository alone does not publish the npm package.
