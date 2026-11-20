package app

import (
	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/credentials"
	"github.com/accessdurin/cli/internal/ports"
)

// credentialService is the service name of every entry the CLI keeps in the OS credential
// store.
const credentialService = "com.getdurin.cli"

// NativeCredentialStore is NativeCredentialStore (src/credentials.ts): the OS credential
// store, whose failures are expected errors that say to unlock it.
type NativeCredentialStore struct{ store credentials.Store }

var _ ports.CredentialStore = NativeCredentialStore{}

// NewNativeCredentialStore returns the OS credential store for the CLI's service.
func NewNativeCredentialStore() NativeCredentialStore {
	return NativeCredentialStore{credentials.NewNative(credentialService)}
}

func unavailableStore(err error) error {
	if err == nil {
		return nil
	}
	return clierror.Wrap(credentials.UnavailableMessage, err)
}

// Read returns the entry's value; found is false when it is absent.
func (s NativeCredentialStore) Read(key string) (string, bool, error) {
	value, found, err := s.store.Read(key)
	return value, found, unavailableStore(err)
}

// Write creates or replaces the entry.
func (s NativeCredentialStore) Write(key, value string) error {
	return unavailableStore(s.store.Write(key, value))
}

// Remove deletes the entry; a missing entry is not an error.
func (s NativeCredentialStore) Remove(key string) error {
	return unavailableStore(s.store.Remove(key))
}
