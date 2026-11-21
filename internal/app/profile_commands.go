package app

import (
	"context"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/jsjson"
)

// ProfileCommand is `durin profiles list|use <id>` (src/profile-commands.ts).
func ProfileCommand(options *CliOptions, io *IO) error {
	profiles := NewFileProfiles(options.Directory)
	action := positional(options, 1)
	if action == "list" {
		list, err := profiles.List()
		if err != nil {
			return err
		}
		return PrintJSON(io.Stdout, ProfilesJSON(list))
	}
	if action != "use" {
		return clierror.New("Use durin profiles list or durin profiles use <id>.")
	}
	id := positional(options, 2)
	if id == "" {
		return clierror.New("Provide a profile ID from durin profiles list.")
	}
	if err := profiles.Activate(id); err != nil {
		return err
	}
	return PrintJSON(io.Stdout, jsjson.Obj(jsjson.KV("activeProfile", jsjson.String(id))))
}

// LogoutCommand is `durin logout [--force]` (src/profile-commands.ts): it revokes the
// profile's MCP grant and deletes it, and fails while the server has not confirmed the
// revocation.
func LogoutCommand(ctx context.Context, options *CliOptions, io *IO) error {
	profile, err := LoadSelectedProfile(options)
	if err != nil {
		// A saved profile that is refused (for example, an issuer not spelled exactly) can still
		// be logged out: its local credentials are deleted.
		if refused, ok := clierror.As(err); ok {
			return logoutRefusedProfile(ctx, options, io, refused.Message, NewNativeCredentialStore(), nil)
		}
		return err
	}
	store := NewNativeCredentialStore()
	if err := RetryPendingRevocations(ctx, options.Directory, profile, store, nil); err != nil {
		return err
	}
	result, err := WithProfileLock(options.Directory, profile.ID, func() (SignOutResult, error) {
		marker, err := NewFileRevocationMarker(options.Directory, profile.ID)
		if err != nil {
			return SignOutResult{}, err
		}
		return SignOutMcp(ctx, SignOutOptions{
			Profile: profile, Store: store, Marker: marker, Force: options.Values.Flag("force"),
			Spool: SpoolDirectory(options.Directory, profile.ID),
		})
	}, DefaultLockPatience)
	if err != nil {
		return err
	}
	detail := "Agent configuration is retained. Run durin to sign in again. Other organization MCP grants are unchanged."
	if result.Local == "retained" {
		detail = "The authorization server could not confirm that access was revoked, so sign-out hasn’t finished. Until it does, MCP calls and durin tools are blocked for this profile. Run durin logout again to retry, or durin logout --force to sign out on this computer anyway and lift the block."
	}
	if err := PrintJSON(io.Stdout, jsjson.Obj(
		jsjson.KV("profile", jsjson.String(profile.ID)),
		jsjson.KV("localMcpGrant", jsjson.String(result.Local)),
		jsjson.KV("remoteMcpGrant", jsjson.String(string(result.Remote))),
		jsjson.KV("detail", jsjson.String(detail)),
	)); err != nil {
		return err
	}
	if result.Remote == RevocationFailed {
		io.ExitCode = 1
	}
	return nil
}

// positional is options.positionals[i], "" when absent.
func positional(options *CliOptions, i int) string {
	if i < len(options.Positionals) {
		return options.Positionals[i]
	}
	return ""
}
