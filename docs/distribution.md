# CLI distribution and releases

The repository builds npm, Homebrew, Debian/Ubuntu, and Windows distributions.
New setup features are in CLI 1.1.0. They become available through each channel
only after that version's assets are published; npm publication is separate from
a GitHub release. Until then, use a verified source build. Creating these packaging
files does not publish a release or an APT repository.

## npm and source

Use `npx @accessdurin/cli` or `npm install --global @accessdurin/cli` after npm
publication. The npm package requires Node 24.22.1 or later within Node 24 and an
unlocked OS credential store. Source installation:

```sh
pnpm install --frozen-lockfile
pnpm verify
node dist/cli.js
```

The npm archive includes the built executable and embedded skills. Onboarding
copies its runtime and native credential/diagnostic dependencies out of the npm
cache before writing absolute agent configuration paths.

## Homebrew

The source repository also acts as a tap through its `Formula` directory. Once
the matching release is published:

```sh
brew tap accessdurin/cli https://github.com/accessdurin/cli
brew install accessdurin/cli/durin
durin
```

The generated formula pins the release's npm archive with SHA-256 and depends on
`node@24`. It installs native dependencies on the target machine. The formula's
`brew test` runs `durin --help`. Upgrades use `brew update` and
`brew upgrade accessdurin/cli/durin` after the next formula/source release.

Alternatively, download `durin.rb` from a published release and put it in a tap you
maintain. Do not substitute a development archive under a released version's URL.

## Ubuntu and Debian

Releases produce native `amd64` and `arm64` `.deb` packages. They include the pinned
Node runtime and locked production dependencies under `/opt/durin`; no separate
Node installation is needed. Download the correct architecture's asset and
`SHA256SUMS` from the same published release:

```sh
sha256sum --ignore-missing --check SHA256SUMS
sudo apt install ./durin-cli_1.1.0_amd64.deb
durin
```

Use the `arm64` filename on ARM64. A Debian/Ubuntu desktop session must provide an
unlocked Secret Service credential store (for example GNOME Keyring). The package
declares `libsecret-1-0`, `dbus-user-session`, and runtime library dependencies.
Headless machines without a credential service cannot complete interactive
credential setup. The build matrix uses Ubuntu 24.04; installation on other
Debian/Ubuntu releases requires validating their native libraries and credential
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
installer. The archive includes Node and locked native dependencies. Download
`install-windows.ps1` from the release, inspect it, then run:

```powershell
.\install-windows.ps1 -Version 1.1.0
```

The installer downloads that version's archive and `SHA256SUMS` over HTTPS,
requires a matching SHA-256, extracts and starts `--help` before activation,
retains previous versions under `%LOCALAPPDATA%\DurinCLI\versions`, and adds
`%LOCALAPPDATA%\DurinCLI\bin` to your user PATH. Open a new terminal and run
`durin`. No administrator rights, account answers, or MCP consent are supplied
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

macOS x64/ARM64 and Linux x64/ARM64 `.tar.gz` archives include Node, the CLI,
and locked native dependencies. Verify the archive against `SHA256SUMS`, extract
to a directory you own, and put its `bin` directory on PATH. Keep that directory
available: saved bridges reference its Node executable. The archive is a CLI
runtime, not the native Endpoint app. macOS notarization is not established by
building an archive.

## Build and release

Use the exact Node version in `.node-version` and pnpm version in `package.json`:

```sh
pnpm verify
pnpm package:release
node scripts/release-metadata.mjs release --write-formula
```

`package:release` builds packages for the current OS/architecture, freezes the
production install to `pnpm-lock.yaml`, includes the running Node binary, and
starts the packaged CLI before archiving it. Linux also builds `.deb` packages
with `dpkg-deb`. Packages go in ignored `release/`; temporary staging is ignored.
Packages include the CLI's MIT license, bundled dependency notices, native package
licenses, and the exact Node version's full license at `runtime/LICENSE`. See
[license provenance](../licenses/README.md) when updating dependencies or Node.
The metadata script generates `SHA256SUMS`, `durin.rb`, and the Windows installer.
Review and commit the regenerated `Formula/durin.rb` with the version change.

The [release workflow](../.github/workflows/release.yml) uses the existing full Linux
verification for pull requests and runs `pnpm verify` itself for release builds.
It builds on five native OS/architecture runners, runs the skill/hook and
bridge integration tests there, starts packaged launchers, installs Debian
packages, and exercises the Windows per-user installer. Manual dispatch builds
Actions artifacts without creating a release. Hosted runner results are evidence
only after those jobs actually pass; local Linux tests do not establish native
macOS or Windows behavior.

For a tagged release, the workflow requires the tag to match `package.json` and
the commit to belong to `main`. Set up the `cli-release` GitHub environment with
any desired maintainer review rules. After all packages pass, the final job
checks the Homebrew checksum against the committed formula and creates a
**draft** GitHub release containing all assets. It never publishes that draft or
replaces existing release assets on a retry. Review the draft and publish it
when ready. If the npm archive changes, regenerate the formula and make a new
reviewed version rather than replacing published bytes.

Publish the verified npm archive separately using the organization's npm
publisher credentials or configured trusted-publisher workflow. The GitHub
release workflow does not require or create an npm token. Confirm
`npx @accessdurin/cli --help`, `brew test`, an installed Debian package, and the
Windows installer on their target systems before announcing availability.
