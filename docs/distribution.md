# CLI distribution and releases

The repository builds npm, Homebrew, Debian/Ubuntu, Windows, and portable
distributions of the native Go executable. New setup features are in CLI 1.1.0.
They become available through each channel only after that version's assets are
published; npm publication is separate from a GitHub release. Until then, use a verified source build. Creating these packaging
files does not publish a release or an APT repository.

## npm and source

Use `npx @accessdurin/cli` or `npm install --global @accessdurin/cli` after npm
publication. `@accessdurin/cli` is a small launcher (`bin/durin.js`) that runs the
executable from the matching platform package, `@accessdurin/cli-<os>-<arch>`
(`darwin-arm64`, `darwin-x64`, `linux-arm64`, `linux-x64`, or `win32-x64`), which
npm installs as an optional dependency. The launcher needs Node 18 or later only to
start the executable; installing with `--omit=optional` leaves it nothing to run.
The CLI needs an unlocked OS credential store. Source installation, with the Go
version in `go.mod`:

```sh
go build -o durin ./cmd/durin
./durin
```

The executable embeds the bundled skills. Onboarding copies the executable out of
the npm cache (or wherever it was installed) into the profile directory's runtime
before writing absolute agent configuration paths.

## Homebrew

The source repository also acts as a tap through its `Formula` directory. Once
the matching release is published:

```sh
brew tap accessdurin/cli https://github.com/accessdurin/cli
brew install accessdurin/cli/durin
durin
```

The generated formula is a binary formula: it pins the release's macOS and Linux
x64/ARM64 archives with SHA-256, installs the `durin` executable, and has no Node
dependency. The formula's `brew test` runs `durin --help`. The committed
`Formula/durin.rb` still describes the published TypeScript 1.1.0 release (an npm
archive that depends on `node@24`) until the next release regenerates it.
Upgrades use `brew update` and `brew upgrade accessdurin/cli/durin` after the next
formula/source release.

Alternatively, download `durin.rb` from a published release and put it in a tap you
maintain. Do not substitute a development archive under a released version's URL.

## Ubuntu and Debian

Releases produce native `amd64` and `arm64` `.deb` packages. They install the
executable as `/usr/bin/durin`; no Node installation is needed. Download the
correct architecture's asset and `SHA256SUMS` from the same published release:

```sh
sha256sum --ignore-missing --check SHA256SUMS
sudo apt install ./durin-cli_1.1.0_amd64.deb
durin
```

Use the `arm64` filename on ARM64. A Debian/Ubuntu desktop session must provide an
unlocked Secret Service credential store (for example GNOME Keyring), which the CLI
reaches over the D-Bus session bus. The package depends on `dbus-user-session` and
suggests `gnome-keyring`. Headless machines without a credential service cannot
complete interactive credential setup. The build matrix uses Ubuntu 24.04;
installation on other Debian/Ubuntu releases requires validating their credential
service. There is no plaintext credential fallback.

Upgrade with `sudo apt install ./<new-package>.deb`; remove the executable with
`sudo apt remove durin-cli`. Packages do not run account setup or modify agent
settings in post-install scripts. Removing a package retains home-directory
profiles, credentials, copied MCP runtimes, skills, and hooks. Run the desired
logout/removal steps before uninstalling if you want to disconnect an agent.

This is downloadable `.deb` distribution. A signed APT repository and unattended
repository updates are not configured by these changes.

## Windows

Releases produce `durin-<version>-windows-x64.zip` and a per-user PowerShell
installer. The archive contains `durin.exe`, its license, and third-party notices.
Download `install-windows.ps1` from the release, inspect it, then run:

```powershell
.\install-windows.ps1 -Version 1.1.0
```

The installer downloads that version's archive and `SHA256SUMS` over HTTPS,
requires a matching SHA-256, extracts and starts `durin.exe --help` before
activation, installs it under `%LOCALAPPDATA%\DurinCLI\versions\<version>`,
retaining previous versions, points the `durin.cmd` shim in
`%LOCALAPPDATA%\DurinCLI\bin` at it, and adds that directory to your user PATH.
Open a new terminal and run `durin`. No administrator rights, account answers, or MCP consent are supplied
by the installer. Windows Credential Manager stores MCP credentials.

For offline installation, download the archive and checksums separately:

```powershell
.\install-windows.ps1 -Version 1.1.0 `
  -ArchivePath .\durin-1.1.0-windows-x64.zip `
  -ChecksumPath .\SHA256SUMS
```

The installer refuses to overwrite an already installed version. For removal,
disconnect/remove your integrations if desired, remove the DurinCLI directory,
and remove its `bin` entry from your user PATH. It does not delete profiles or
credentials. Windows ARM64-native installers, MSI, winget registration, and
Authenticode signing are not supplied; the current artifact is x64.

## Portable archives

macOS x64/ARM64 and Linux x64/ARM64 `.tar.gz` archives contain the `durin`
executable, `LICENSE`, `README.md`, and `THIRD_PARTY_LICENSES.md`. Verify the
archive against `SHA256SUMS`, extract it anywhere, and put the directory holding
`durin` on PATH. Setup copies the executable into the profile directory's runtime
before writing agent configurations, so the extracted files need not stay put.
The archive is the CLI, not the native Endpoint app. macOS notarization is not
established by building an archive.

## Build and release

Use the Go version in `go.mod`. On each of the five release platforms (Linux
x64/ARM64, macOS ARM64/x64, Windows x64), run:

```sh
go run ./tools/release package
```

`package` builds the executable for the current OS/architecture (with cgo on macOS,
for the Keychain; pure Go elsewhere), starts it with `--help`, and writes
`durin-<version>-<os>-<arch>.tar.gz` (`.zip` on Windows) containing the
executable, `LICENSE`, `README.md`, and `THIRD_PARTY_LICENSES.md`. Linux also
builds a `durin-cli_<version>_<arch>.deb` with `dpkg-deb`. Every platform also
packs its npm platform package, `@accessdurin/cli-<os>-<arch>`, with `npm pack`.
Packages go in ignored `release/`; temporary staging in `.release-stage/` is
ignored. The version comes from `internal/version/version.go`.
`THIRD_PARTY_LICENSES.md` is generated from the license files of the Go modules
linked into that platform's executable plus the notices of code ported into this
repository (`internal/*/LICENSE-*`). See [license provenance](../licenses/README.md)
when updating dependencies.

With every platform's packages in one `release/` directory, run:

```sh
go run ./tools/release metadata -write-formula
```

`metadata` writes the npm launcher package `@accessdurin/cli`, the Homebrew
formula `durin.rb` (and, with `-write-formula`, `Formula/durin.rb`), copies
`install-windows.ps1`, and generates `SHA256SUMS` over every asset. The formula
pins each archive's SHA-256, so it must be generated from the same archives the
release ships. Review and commit the regenerated `Formula/durin.rb` with the
version change.

The [release workflow](../.github/workflows/release.yml) relies on the full
verification in [verify.yml](../.github/workflows/verify.yml) for pull requests and
runs vet, the tests, and the black-box parity harness itself for release builds.
It builds on five native OS/architecture runners, runs the credential-store and
agent integration tests there, packages and starts each executable, installs the
Debian package, and exercises the Windows per-user installer. It then generates
the metadata, installs the generated Homebrew formula from the built archives, and
tries the npm launcher with the macOS ARM64 platform package. Manual dispatch
builds Actions artifacts without creating a release. Hosted runner results are
evidence only after those jobs actually pass; local Linux tests do not establish
native macOS or Windows behavior.

For a tagged release, the workflow requires the tag to match the version in
`internal/version/version.go` and the commit to belong to `main`. Set up the
`cli-release` GitHub environment with any desired maintainer review rules. After
all packages pass, the final job checks that the committed `Formula/durin.rb`
matches the generated formula and creates a **draft** GitHub release containing
all assets. It never publishes that draft or replaces existing release assets on a
retry. Review the draft and publish it when ready. If an archive changes,
regenerate the formula and make a new reviewed version rather than replacing
published bytes.

Publish the npm packages separately from the release assets, the five platform
packages first and then the launcher; see [Publish to npm](../README.md#publish-to-npm).
The GitHub release workflow does not require or create an npm token. Confirm
`npx @accessdurin/cli --help`, `brew test`, an installed Debian package, and the
Windows installer on their target systems before announcing availability.
