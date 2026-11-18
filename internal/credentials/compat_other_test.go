//go:build !linux

package credentials

import "testing"

// compareSecretServiceItems is Linux-only; compat_test.go calls it only there.
func compareSecretServiceItems(*testing.T, string, string, string) {}
