// Package credentials stores secrets in the operating system's credential
// store, using exactly the entries the TypeScript CLI writes through
// @napi-rs/keyring 2.1.0, so grants survive an upgrade and a downgrade.
//
// @napi-rs/keyring 2.1.0 is built on keyring-core 1.0.0 and these stores
// (crate versions read from the published native binaries):
//
//   - Linux: dbus-secret-service-keyring-store 1.0.1 over dbus-secret-service
//     4.1.0, pinned by the CLI with { linux: { store: 'secret-service' } }.
//   - Windows: windows-native-keyring-store 1.1.0, default configuration.
//   - macOS: apple-native-keyring-store 1.0.2, "keychain" module, default
//     (User domain) keychain.
//
// The per-OS files document the exact layout they mirror. There is no
// plaintext or file fallback, on purpose: when the native store cannot be
// used, every operation fails with [ErrUnavailable].
package credentials

import (
	"errors"
	"unicode/utf8"
)

// UnavailableMessage is the exact text of every credential store failure,
// identical to the TypeScript CLI's message.
const UnavailableMessage = "OS credential store unavailable. Unlock your credential store and run durin again."

// ErrUnavailable is matched (with errors.Is) by every error this package
// returns. Its message is exactly [UnavailableMessage].
var ErrUnavailable = errors.New(UnavailableMessage)

// Store is the credential store port, mirroring src/ports/credential-store.ts.
type Store interface {
	// Read returns the saved value. A missing entry is not an error: it
	// returns found == false.
	Read(key string) (value string, found bool, err error)
	// Write creates the entry or replaces its value.
	Write(key, value string) error
	// Remove deletes the entry. Removing a missing entry succeeds, as
	// @napi-rs/keyring's deleteCredential resolves false instead of
	// rejecting when there is nothing to delete.
	Remove(key string) error
}

// NewNative returns the OS credential store for the given service name.
// Callers pass "com.getdurin.cli".
func NewNative(service string) Store {
	return nativeStore{service: service}
}

// backend is one OS's store. get reports a missing entry as found == false
// and remove reports it as nil; any other failure is a non-nil error.
type backend interface {
	get(service, key string) (value string, found bool, err error)
	set(service, key, value string) error
	remove(service, key string) error
}

type nativeStore struct {
	service string
}

func (s nativeStore) Read(key string) (string, bool, error) {
	if err := validUTF8(s.service, key); err != nil {
		return "", false, unavailable(err)
	}
	value, found, err := platform.get(s.service, key)
	if err != nil {
		return "", false, unavailable(err)
	}
	return value, found, nil
}

func (s nativeStore) Write(key, value string) error {
	if err := validUTF8(s.service, key, value); err != nil {
		return unavailable(err)
	}
	if err := platform.set(s.service, key, value); err != nil {
		return unavailable(err)
	}
	return nil
}

func (s nativeStore) Remove(key string) error {
	if err := validUTF8(s.service, key); err != nil {
		return unavailable(err)
	}
	if err := platform.remove(s.service, key); err != nil {
		return unavailable(err)
	}
	return nil
}

// validUTF8 rejects strings the TypeScript CLI could never pass: napi-rs
// hands Rust a valid UTF-8 string, so writing anything else would create an
// entry the TypeScript CLI cannot read back.
func validUTF8(values ...string) error {
	for _, v := range values {
		if !utf8.ValidString(v) {
			return errors.New("credentials: string is not valid UTF-8")
		}
	}
	return nil
}

// unavailableError reports a backend failure with the user-facing message
// while keeping the cause for errors.Unwrap. Causes never contain secrets.
type unavailableError struct {
	cause error
}

func unavailable(cause error) error {
	return &unavailableError{cause: cause}
}

func (e *unavailableError) Error() string        { return UnavailableMessage }
func (e *unavailableError) Unwrap() error        { return e.cause }
func (e *unavailableError) Is(target error) bool { return target == ErrUnavailable }
