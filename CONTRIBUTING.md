# Contributing to Durin CLI

Bug reports, documentation fixes, and pull requests are welcome. You can build and
test this public repository without a Durin account, production credentials, or
access to another repository. Interactive account setup needs a configured Durin
deployment and an unlocked OS credential store.

## Local development

Use the Node version in [.node-version](.node-version) and pnpm version in
`package.json`'s `packageManager` field. Install that pnpm version with your usual
Node package manager, then:

```sh
git clone https://github.com/accessdurin/cli.git
cd cli
pnpm install --frozen-lockfile
pnpm verify
node dist/cli.js --help
```

`pnpm verify` runs formatting, lint, types, all discovered tests, the production
build, and the credential-pattern check. Tests use temporary files, local servers,
and synthetic credentials. The real credential-store round trip is optional;
see the [development instructions](README.md#development-and-verification).
Run `pnpm format` to apply the repository's formatting.

## Changes and tests

Keep production dependency versions exact in `dependencies`. Preserve ranges in
`devDependencies` and the pinned pnpm tool version. Update `pnpm-lock.yaml` with any
dependency change and verify a frozen install. Generated bundles and release
archives stay out of Git.

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

For public bug reports, include the CLI version or commit, OS, Node version,
command shape, and redacted reproduction steps. Do not attach tokens, private
profile directories, or tool inputs containing confidential data. Use
[SECURITY.md](SECURITY.md) for vulnerabilities.

## License and attribution

Contributions are provided under this repository's [MIT license](LICENSE). Retain
upstream copyright and license notices when adding third-party material. See
[licenses/README.md](licenses/README.md) when updating bundled skills, dependencies,
or the Node runtime.
