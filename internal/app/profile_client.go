package app

import (
	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
)

// ConnectionProfile is the first step of profileConnection (src/profile-client.ts): the
// selected (or active) profile, with any failure to load it reported as one expected error.
// The rest of profileConnection (pending revocations, the OAuth provider and the secure fetch)
// belongs to the MCP step of the port.
func ConnectionProfile(options *CliOptions) (contracts.CliProfile, error) {
	profile, err := LoadSelectedProfile(options)
	if err != nil {
		return contracts.CliProfile{}, clierror.Wrap("No valid organization profile. Run durin in a terminal to complete setup.", err)
	}
	return profile, nil
}

// LoadSelectedProfile is new FileProfiles(directory).load(options.values.profile): the
// --profile given (even an empty one, which is invalid), else the active profile.
func LoadSelectedProfile(options *CliOptions) (contracts.CliProfile, error) {
	profiles := NewFileProfiles(options.Directory)
	if id, given := options.Values.String("profile"); given {
		return profiles.load(id)
	}
	return profiles.Load("")
}
