package app

import (
	"context"
	"io"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/mcp/httpx"
)

// ConnectionProfile is the first step of profileConnection (src/profile-client.ts): the
// selected (or active) profile, with any failure to load it reported as one expected error.
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

// withSignedInProfile runs work under the profile lock once sign-out is known to be finished,
// so no request slips past a logout that is still revoking.
func withSignedInProfile[T any](directory, profileID string, work func() (T, error)) (T, error) {
	return WithProfileLock(directory, profileID, func() (T, error) {
		var zero T
		marker, err := NewFileRevocationMarker(directory, profileID)
		if err != nil {
			return zero, err
		}
		if err := AssertSignOutFinished(marker); err != nil {
			return zero, err
		}
		return work()
	}, DefaultLockPatience)
}

// ProfileConnection is what an MCP session for the selected profile needs (profileConnection
// in src/profile-client.ts). Requests reach only the profile's hosts.
type ProfileConnection struct {
	Profile       contracts.CliProfile
	Resource      string
	Development   bool
	Marker        *FileRevocationMarker
	Authorization *McpOAuthProvider
	Fetch         httpx.Fetch
	// Warn writes the SDK's warnings through the sanitized console.
	Warn func(string)
	// Serialize runs work under the profile lock once sign-out is known to be finished.
	Serialize func(work func() error) error
}

// NewProfileConnection loads the selected profile, retries revocations an earlier sign-in
// left pending, and prepares its OAuth provider and secure fetch. stderr receives the SDK's
// warnings.
func NewProfileConnection(ctx context.Context, options *CliOptions, stderr io.Writer) (*ProfileConnection, error) {
	profile, err := ConnectionProfile(options)
	if err != nil {
		return nil, err
	}
	store := NewNativeCredentialStore()
	if err := RetryPendingRevocations(ctx, options.Directory, profile, store, nil); err != nil {
		return nil, err
	}
	marker, err := NewFileRevocationMarker(options.Directory, profile.ID)
	if err != nil {
		return nil, err
	}
	allowlist := contracts.ProfileHostAllowlist(profile.Resource, profile.McpIssuer, profile.Development)
	return &ProfileConnection{
		Profile:       profile,
		Resource:      profile.Resource,
		Development:   profile.Development,
		Marker:        marker,
		Authorization: NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store, Marker: marker}),
		Fetch:         HTTPFetch(SecureFetch(allowlist, nil)),
		Warn:          ConsoleWarn(stderr),
		Serialize: func(work func() error) error {
			_, err := withSignedInProfile(options.Directory, profile.ID, func() (struct{}, error) { return struct{}{}, work() })
			return err
		},
	}, nil
}

// WithProfileClient is withProfileClient (src/profile-client.ts): work runs on a connected
// client for the selected profile, under the profile lock.
func WithProfileClient[T any](ctx context.Context, options *CliOptions, stderr io.Writer, work func(client *RemoteMcpClient) (T, error)) (T, error) {
	var zero T
	connection, err := NewProfileConnection(ctx, options, stderr)
	if err != nil {
		return zero, err
	}
	return withSignedInProfile(options.Directory, connection.Profile.ID, func() (T, error) {
		client, err := NewRemoteMcpClient(RemoteMcpOptions{
			Resource:      connection.Resource,
			Authorization: connection.Authorization,
			Fetch:         connection.Fetch,
			Development:   connection.Development,
			Warn:          connection.Warn,
		})
		if err != nil {
			return zero, err
		}
		defer client.Close()
		if err := client.Connect(ctx); err != nil {
			return zero, err
		}
		return work(client)
	})
}
