package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/mcp/schema"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// RevocationStatus is what the authorization server said about a grant: revoked,
// unavailable (it offers no revocation, or there was nothing to revoke) or failed.
type RevocationStatus string

const (
	RevocationRevoked     RevocationStatus = "revoked"
	RevocationUnavailable RevocationStatus = "unavailable"
	RevocationFailed      RevocationStatus = "failed"
	// revocationRetry: the server did not confirm, so the grant may still be honoured.
	revocationRetry RevocationStatus = "retry"
)

// SignOutResult is the outcome of signOutMcp: Local is "cleared" or "retained".
type SignOutResult struct {
	Remote RevocationStatus
	Local  string
}

// SignOutOptions configure SignOutMcp. Fetch is the HTTP client under the profile's secure
// fetch (nil: http.DefaultClient).
type SignOutOptions struct {
	Profile contracts.CliProfile
	Store   ports.CredentialStore
	Marker  ports.RevocationMarker
	Fetch   *http.Client
	Force   bool
}

// RetireOptions configure RetireKeptGrant. Fetch is as in SignOutOptions.
type RetireOptions struct {
	Profile contracts.CliProfile
	Store   ports.CredentialStore
	Pending ports.PendingRevocations
	Fetch   *http.Client
}

var revocationSchema = zod.Object(zod.F("revocation_endpoint", zod.Optional(contracts.URL())))

// transientRevocationError: the authorization server did not confirm the revocation, so the
// grant may still be honoured and revocation should be retried. Captive portals and proxies
// answer with redirects and 4xx pages, so only a 2xx from the revocation endpoint counts as
// revoked.
type transientRevocationError struct {
	message string
	cause   error
}

func (e *transientRevocationError) Error() string { return e.message }
func (e *transientRevocationError) Unwrap() error { return e.cause }

func retryableStatus(status int) bool { return status == 429 || status >= 500 }

// transientFailures: only a refused unsafe destination is final; any other failure to reach
// the server, a refused redirect included, is not, and neither is a 429 or 5xx answer.
func transientFailures(fetch httpx.Fetch) httpx.Fetch {
	return func(ctx context.Context, url string, init *httpx.RequestInit) (*http.Response, error) {
		resp, err := fetch(ctx, url, init)
		if err != nil {
			if _, final := err.(*clierror.Error); final {
				return nil, err
			}
			return nil, &transientRevocationError{"MCP authorization server unreachable.", err}
		}
		if !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		_ = resp.Body.Close()
		return nil, &transientRevocationError{message: fmt.Sprintf("MCP authorization server returned %d.", resp.StatusCode)}
	}
}

// unparseable: a 2xx body that is not metadata may come from a portal or proxy rather than
// the server.
func unparseable(err error) bool {
	var syntax *jsjson.SyntaxError
	var invalid *schema.Error
	return errors.As(err, &syntax) || errors.As(err, &invalid)
}

func isTransient(err error) bool {
	var transient *transientRevocationError
	return errors.As(err, &transient)
}

// SignOutMcp is signOutMcp (src/logout.ts). Only a transient revocation failure keeps the
// grant, unless forced, so logout can retry. The profile is marked before any request and
// cleared only once its credentials are gone, so a logout that is interrupted or fails leaves
// the profile blocked until sign-out finishes. A profile that cannot be marked cannot be
// blocked, so its grant is never kept.
func SignOutMcp(ctx context.Context, options SignOutOptions) (SignOutResult, error) {
	provider := NewMcpOAuthProvider(McpOAuthOptions{Profile: options.Profile, Store: options.Store})
	blocked := options.Marker.Mark() == nil
	outcome := RevocationFailed
	if grant, err := readStoredGrant(ctx, provider); err == nil {
		if grant == nil {
			outcome = RevocationUnavailable
		} else if status, err := revokeGrant(ctx, options.Profile, *grant, options.Fetch); err == nil {
			outcome = status
		}
	}
	if outcome == revocationRetry && blocked && !options.Force {
		return SignOutResult{Remote: RevocationFailed, Local: "retained"}, nil
	}
	if err := provider.InvalidateCredentials(ctx, oauth.InvalidateAll); err != nil {
		return SignOutResult{}, err
	}
	if err := options.Marker.Clear(); err != nil {
		return SignOutResult{}, err
	}
	if outcome == revocationRetry {
		outcome = RevocationFailed
	}
	return SignOutResult{Remote: outcome, Local: "cleared"}, nil
}

// RetireKeptGrant is retireKeptGrant (src/logout.ts). It runs before a sign-in that replaces a
// grant an unfinished logout kept: the kept grant is revoked, or handed to the pending list
// when the server cannot confirm that, and then deleted, so the new sign-in can never refresh
// or reuse it.
func RetireKeptGrant(ctx context.Context, options RetireOptions) error {
	provider := NewMcpOAuthProvider(McpOAuthOptions{Profile: options.Profile, Store: options.Store})
	grant, err := readStoredGrant(ctx, provider)
	if err != nil {
		grant = nil
	}
	if grant != nil && !revocationFinished(ctx, options.Profile, *grant, options.Fetch) {
		if err := options.Pending.Add(*grant); err != nil {
			return err
		}
	}
	return provider.InvalidateCredentials(ctx, oauth.InvalidateAll)
}

// pendingRetryDeadline: a command spends at most this long on the pending list, so it is never
// held up for long.
const pendingRetryDeadline = 10 * time.Second

// RetryPendingRevocations is retryPendingRevocations (src/logout.ts), run by commands that use
// a profile. Best effort: it retries revoking grants that an earlier sign-in replaced and
// forgets each one only once the server revokes it or offers no revocation. It never waits for
// a busy profile and never fails the command, so it returns an error only for a profile whose
// pending list cannot exist (an invalid id).
func RetryPendingRevocations(ctx context.Context, directory string, profile contracts.CliProfile, store ports.CredentialStore, fetch *http.Client) error {
	pending, err := NewFilePendingRevocations(directory, profile.ID, store)
	if err != nil {
		return err
	}
	if waiting, err := pending.Entries(); err != nil || len(waiting) == 0 {
		return nil
	}
	_, _ = WithProfileLock(directory, profile.ID, func() (struct{}, error) {
		ctx, cancel := context.WithTimeout(ctx, pendingRetryDeadline)
		defer cancel()
		grants, err := pending.Entries()
		if err != nil {
			return struct{}{}, err
		}
		remaining := []contracts.UnrevokedGrant{}
		for _, grant := range grants {
			if !revocationFinished(ctx, profile, grant, fetch) {
				remaining = append(remaining, grant)
			}
		}
		return struct{}{}, pending.Replace(remaining)
	}, 0)
	return nil
}

func readStoredGrant(ctx context.Context, provider *McpOAuthProvider) (*contracts.UnrevokedGrant, error) {
	tokens, err := provider.Tokens(ctx, nil)
	if err != nil || tokens == nil {
		return nil, err
	}
	client, err := provider.ClientInformation(ctx, nil)
	if err != nil || client == nil {
		return nil, err
	}
	grant := &contracts.UnrevokedGrant{ClientID: client.ClientID, AccessToken: tokens.AccessToken}
	if tokens.RefreshToken != nil {
		grant.RefreshToken = *tokens.RefreshToken
	}
	return grant, nil
}

// revocationFinished: only a revoked grant, or a server without revocation, can be forgotten
// safely.
func revocationFinished(ctx context.Context, profile contracts.CliProfile, grant contracts.UnrevokedGrant, base *http.Client) bool {
	status, err := revokeGrant(ctx, profile, grant, base)
	return err == nil && (status == RevocationRevoked || status == RevocationUnavailable)
}

// revokeGrant reports revocationRetry when the server did not confirm the revocation.
func revokeGrant(ctx context.Context, profile contracts.CliProfile, grant contracts.UnrevokedGrant, base *http.Client) (RevocationStatus, error) {
	allowlist := contracts.ProfileHostAllowlist(profile.Resource, profile.McpIssuer, profile.Development)
	guarded := transientFailures(HTTPFetch(SecureFetch(allowlist, base)))
	status, err := revokeRemotely(ctx, guarded, grant, profile)
	if err != nil {
		if isTransient(err) {
			return revocationRetry, nil
		}
		return "", err
	}
	return status, nil
}

func revokeRemotely(ctx context.Context, fetch httpx.Fetch, grant contracts.UnrevokedGrant, profile contracts.CliProfile) (RevocationStatus, error) {
	metadata, err := oauth.DiscoverAuthorizationServerMetadata(ctx, profile.McpIssuer, oauth.AuthorizationServerDiscoveryOptions{Fetch: fetch})
	if err != nil {
		if unparseable(err) {
			return "", &transientRevocationError{"MCP authorization server metadata was unreadable.", err}
		}
		return "", err
	}
	// Discovery gives up when every metadata URL answers 4xx, which a portal or proxy may send.
	if metadata == nil {
		return "", &transientRevocationError{message: "MCP authorization server metadata was not found."}
	}
	// Only the server's own metadata can say that it offers no revocation.
	parsed, err := zod.Parse(revocationSchema, metadata.Raw)
	if err != nil {
		return "", err
	}
	endpoint, ok := parsed.(*jsjson.Object).GetString("revocation_endpoint")
	if !ok || endpoint == "" {
		return RevocationUnavailable, nil
	}
	for _, token := range [][2]string{{grant.RefreshToken, "refresh_token"}, {grant.AccessToken, "access_token"}} {
		if err := revokeToken(ctx, fetch, endpoint, grant.ClientID, token[0], token[1]); err != nil {
			return "", err
		}
	}
	return RevocationRevoked, nil
}

func revokeToken(ctx context.Context, fetch httpx.Fetch, endpoint, clientID, token, hint string) error {
	if token == "" {
		return nil
	}
	header := http.Header{}
	header.Set("content-type", "application/x-www-form-urlencoded")
	body := weburl.NewSearchParams([2]string{"token", token}, [2]string{"token_type_hint", hint}, [2]string{"client_id", clientID}).String()
	resp, err := fetch(ctx, endpoint, &httpx.RequestInit{Method: http.MethodPost, Header: header, Body: []byte(body)})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if !httpx.OK(resp) {
		return &transientRevocationError{message: fmt.Sprintf("MCP grant revocation returned %d.", resp.StatusCode)}
	}
	return nil
}
