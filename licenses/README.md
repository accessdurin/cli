# License provenance and distribution

The repository's [MIT license](../LICENSE) covers Durin code and documentation.
The Claude plugin carries the same license in its own distribution directory.
Third-party software retains its own terms; the root MIT license does not
relicense dependencies or Node.js.

## CLI dependencies and copied runtimes

`pnpm build` emits `dist/THIRD_PARTY_LICENSES.md` from the dependencies actually
included by Vite. The npm archive includes that file, and portable archives and
Debian packages preserve it beside `dist/cli.js`. External native packages and
their dependencies retain the license files from their installed packages.
Setup copies the CLI license and bundled notices into each installed MCP runtime,
alongside the external packages and their license files.

Review the generated notices after a dependency update. Check license changes,
missing notices, and any additional upstream attribution requirements before
publishing. Generated output belongs in the built package rather than Git.

## Bundled skills

The embedded skills come from
[accessdurin/skills at a28e6b9905fea11deb5b1b66ce4be608fc0255ef](https://github.com/accessdurin/skills/tree/a28e6b9905fea11deb5b1b66ce4be608fc0255ef).
That snapshot's [LICENSE](https://github.com/accessdurin/skills/blob/a28e6b9905fea11deb5b1b66ce4be608fc0255ef/LICENSE)
is MIT, copyright 2026 Durin contributors. `scripts/sync-skills.mjs` embeds the
reviewed checkout's license with its revision and skill hashes. Setup installs a
`LICENSE` alongside each `SKILL.md`. When syncing another revision, review both
the instructions and its license.

## Node.js in OS packages

[node-v24.22.1-LICENSE](node-v24.22.1-LICENSE) is an unmodified copy of
[Node.js v24.22.1's LICENSE](https://github.com/nodejs/node/blob/v24.22.1/LICENSE),
including the notices for libraries bundled into Node. Release packaging copies
the version-matched file to `runtime/LICENSE` beside the Node executable. The npm
archive uses the consumer's Node installation and does not bundle Node.

When changing `.node-version`, obtain the full LICENSE from that exact upstream
Node tag, save it as `licenses/node-v<version>-LICENSE`, and review the changes.
Packaging fails if the corresponding file is missing. Keep the text verbatim.
