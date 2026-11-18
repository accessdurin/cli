// Package version holds the CLI's release version and the license installed beside its
// runtime copy.
package version

import _ "embed"

// Version is the CLI's release version (tools/release reads it from this line; override with
// -ldflags "-X github.com/accessdurin/cli/internal/version.Version=...").
var Version = "1.1.0"

// License is the repository's LICENSE, which InstallRuntime copies next to the installed
// executable. A test keeps it identical to the repository root's.
//
//go:embed LICENSE
var License string
