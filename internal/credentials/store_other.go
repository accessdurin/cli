//go:build !linux && !windows && !(darwin && cgo)

package credentials

import "errors"

// The CLI ships for Linux, macOS and Windows only. A macOS build without
// cgo cannot reach the Security framework, so it reports the store as
// unavailable rather than storing secrets anywhere else; release builds for
// macOS must set CGO_ENABLED=1.
var platform backend = unsupported{}

var errUnsupported = errors.New("credentials: no native credential store for this platform build")

type unsupported struct{}

func (unsupported) get(string, string) (string, bool, error) { return "", false, errUnsupported }
func (unsupported) set(string, string, string) error         { return errUnsupported }
func (unsupported) remove(string, string) error              { return errUnsupported }
