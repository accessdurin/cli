# License provenance and distribution

The repository's [MIT license](../LICENSE) covers Durin code and documentation.
The Claude plugin carries the same license in its own distribution directory.
Third-party software retains its own terms; the root MIT license does not
relicense dependencies or code ported from other projects.

## CLI dependencies and copied runtimes

`go run ./tools/release package` generates `THIRD_PARTY_LICENSES.md` from the
license files of the Go modules actually linked into that platform's executable,
followed by the notices of ported code (below). `go run ./tools/release licenses`
prints the same notices for a given `-goos` and `-goarch`. Portable archives and
npm platform packages include the file beside the executable; Debian packages
install it under `/usr/share/doc/durin-cli/` and the generated Homebrew formula in its
documentation directory. Setup copies the CLI license into each installed MCP
runtime, and the notices too when they sit beside the executable it copies.

Review the generated notices after a dependency update. Check license changes,
missing notices, and any additional upstream attribution requirements before
publishing. Generated output belongs in the built package rather than Git.

## Bundled skills

The embedded skills come from
[accessdurin/skills at a28e6b9905fea11deb5b1b66ce4be608fc0255ef](https://github.com/accessdurin/skills/tree/a28e6b9905fea11deb5b1b66ce4be608fc0255ef).
That snapshot's [LICENSE](https://github.com/accessdurin/skills/blob/a28e6b9905fea11deb5b1b66ce4be608fc0255ef/LICENSE)
is MIT, copyright 2026 Durin contributors. `go run ./tools/syncskills` embeds the
reviewed checkout's license with its revision and skill hashes. Setup installs a
`LICENSE` alongside each `SKILL.md`. When syncing another revision, review both
the instructions and its license.

## Ported code

Some packages port code from other projects so that the CLI behaves byte for byte
like the TypeScript CLI it replaced; see [the Go port](../docs/go-port.md). Their
upstream notices are kept as `internal/<package>/LICENSE-<project>` files, such as
[jsonc-parser](../internal/jsonc/LICENSE-jsonc-parser.md) and
[ada](../internal/weburl/LICENSE-ada.md), and release packaging appends every such
file to `THIRD_PARTY_LICENSES.md`. When porting more code, add its upstream license
in the same way and keep the text verbatim.
