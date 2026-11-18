# Contributing to Durin CLI

Bug reports, documentation fixes, and pull requests are welcome. You can build and
test this public repository without a Durin account, production credentials, or
access to another repository. Interactive account setup needs a configured Durin
deployment and an unlocked OS credential store.

## Local development

Use the Go version in [go.mod](go.mod), then:

```sh
git clone https://github.com/accessdurin/cli.git
cd cli
gofmt -l .   # must print nothing
go vet ./...
go test ./internal/... ./cmd/... ./tools/...
e2e/run.sh
go run ./tools/checksecrets
go build -o durin ./cmd/durin
./durin --help
```

These are the checks CI runs in [verify.yml](.github/workflows/verify.yml), which
also cross-compiles for Windows, macOS, and Linux ARM64. Tests use temporary files,
local servers, and synthetic credentials. `e2e/run.sh` is the black-box parity
harness: it starts a private D-Bus session and keyring and needs `dbus-daemon`,
`gnome-keyring-daemon`, and `secret-tool` (Debian/Ubuntu packages `dbus`,
`gnome-keyring`, and `libsecret-tools`). The real credential-store round trip is
optional; see the [development instructions](README.md#development-and-verification).
Run `gofmt -w .` to apply the repository's formatting. [The Go port](docs/go-port.md)
describes the package layout and how parity with the TypeScript CLI is checked.

## Changes and tests

Commit `go.mod` and `go.sum` together with any dependency change, and review the
generated third-party notices (`go run ./tools/release licenses`). Built executables
and release archives stay out of Git.

Follow [AGENTS.md](AGENTS.md) to keep tests lean: extend an existing case before
adding overlapping setup, and preserve security and regression assertions.
Onboarding follows server responses. Avoid adding local completion flags; see the
[state audit](docs/cli-state-audit.md) for existing lifecycle boundaries.

Include the problem, resulting behavior, and actual validation in your PR. PR
titles must include a circle emoji, for example `🟡 Fix interrupted setup`.
Describe any platform-specific checks you could not run. Release packaging and
hosted authentication require additional verification on their target systems;
passing local tests does not establish that a hosted service or every harness is
ready. See the [release guide](docs/distribution.md) for maintainer publishing.

For public bug reports, include the CLI version or commit, OS, how you installed
the CLI, command shape, and redacted reproduction steps. Do not attach tokens, private
profile directories, or tool inputs containing confidential data. Use
[SECURITY.md](SECURITY.md) for vulnerabilities.

## License and attribution

Contributions are provided under this repository's [MIT license](LICENSE). Retain
upstream copyright and license notices when adding third-party material. See
[licenses/README.md](licenses/README.md) when updating bundled skills, dependencies,
or ported code.
