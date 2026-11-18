package app

import (
	"context"
	"net/http"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// providerRequestTimeout bounds each WorkOS request, body included.
const providerRequestTimeout = 15 * time.Second

// DeviceAuthOptions configure WorkOS device sign-in for a public client.
type DeviceAuthOptions struct {
	ClientID    string
	Origin      string
	Fetch       *http.Client
	Clock       ports.PollingClock
	Development bool
}

// deviceChallenge is deviceChallengeSchema's output.
type deviceChallenge struct {
	DeviceCode              string  `json:"device_code"`
	UserCode                string  `json:"user_code"`
	VerificationURI         string  `json:"verification_uri"`
	VerificationURIComplete *string `json:"verification_uri_complete"`
	ExpiresIn               int64   `json:"expires_in"`
	Interval                int64   `json:"interval"`
}

// pollDelays is how much longer to wait after each error that means "keep polling".
var pollDelays = map[string]time.Duration{"authorization_pending": 0, "slow_down": 5 * time.Second}

func nextInterval(body jsjson.Value, interval time.Duration) (time.Duration, error) {
	parsed, err := zod.Parse(contracts.DeviceErrorSchema, body)
	if err != nil {
		return 0, err
	}
	code, _ := parsed.(*jsjson.Object).GetString("error")
	increment, ok := pollDelays[code]
	if !ok {
		return 0, clierror.New("Account authorization failed. Run durin to sign in again.")
	}
	return interval + increment, nil
}

// browserURL is the page to approve the device on, refused unless it is a safe CLI URL once
// its query is removed.
func browserURL(challenge deviceChallenge, development bool) (string, error) {
	raw := challenge.VerificationURI
	if challenge.VerificationURIComplete != nil {
		raw = *challenge.VerificationURIComplete
	}
	url, err := weburl.Parse(raw)
	if err != nil {
		return "", err
	}
	base := url.Clone()
	base.SetSearch("")
	if !contracts.SafeCliURL(base.Href(), development) {
		return "", clierror.New("Unsafe account authorization URL.")
	}
	return url.Href(), nil
}

func sameOrganization(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func boundSession(next, previous contracts.AccountSession, organizationID *string) (contracts.AccountSession, error) {
	if next.PrincipalID != previous.PrincipalID || !sameOrganization(next.OrganizationID, organizationID) {
		return contracts.AccountSession{}, clierror.New("Refreshed account does not match the selected profile.")
	}
	return next, nil
}

// WorkosDeviceAuth is WorkOS's OAuth device flow for a public client (src/device-auth.ts).
type WorkosDeviceAuth struct {
	options DeviceAuthOptions
}

var _ ports.DeviceAccountAuth = (*WorkosDeviceAuth)(nil)

// NewWorkosDeviceAuth returns device sign-in for options.
func NewWorkosDeviceAuth(options DeviceAuthOptions) *WorkosDeviceAuth {
	return &WorkosDeviceAuth{options: options}
}

// Refresh rotates the session's refresh token, into organizationID when it is not nil (else
// the session's organization). The result must keep the principal and land in that
// organization.
func (a *WorkosDeviceAuth) Refresh(ctx context.Context, session contracts.AccountSession, organizationID *string) (contracts.AccountSession, error) {
	if organizationID == nil {
		organizationID = session.OrganizationID
	}
	fields := [][2]string{{"grant_type", "refresh_token"}, {"refresh_token", session.RefreshToken}}
	if organizationID != nil && *organizationID != "" {
		fields = append(fields, [2]string{"organization_id", *organizationID})
	}
	resp, err := a.post(ctx, "/user_management/authenticate", fields, providerRequestTimeout)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	if !responseOK(resp) {
		resp.Body.Close()
		return contracts.AccountSession{}, clierror.New("Account session could not be refreshed. Run durin to sign in again.")
	}
	body, err := responseJSON(resp)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	next, err := contracts.ParseDeviceSession(body)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	return boundSession(next, session, organizationID)
}

// Authorize starts device sign-in, shows the code and URL with present, then polls until
// the user approves, the provider refuses, or the challenge expires.
func (a *WorkosDeviceAuth) Authorize(ctx context.Context, present func(contracts.AuthorizationPrompt) error) (contracts.AccountSession, error) {
	resp, err := a.post(ctx, "/user_management/authorize/device", nil, providerRequestTimeout)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	if !responseOK(resp) {
		resp.Body.Close()
		return contracts.AccountSession{}, clierror.New("Unable to start account sign-in.")
	}
	body, err := responseJSON(resp)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	parsed, err := zod.Parse(contracts.DeviceChallengeSchema, body)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	var challenge deviceChallenge
	if err := jsjson.Decode(parsed, &challenge); err != nil {
		return contracts.AccountSession{}, err
	}
	deadline := a.options.Clock.Now().Add(time.Duration(challenge.ExpiresIn) * time.Second)
	url, err := browserURL(challenge, a.options.Development)
	if err != nil {
		return contracts.AccountSession{}, err
	}
	if err := present(contracts.AuthorizationPrompt{Code: challenge.UserCode, URL: url}); err != nil {
		return contracts.AccountSession{}, err
	}
	return a.poll(ctx, challenge, deadline)
}

func (a *WorkosDeviceAuth) poll(ctx context.Context, challenge deviceChallenge, deadline time.Time) (contracts.AccountSession, error) {
	clock := a.options.Clock
	interval := time.Duration(challenge.Interval) * time.Second
	for {
		if err := a.assertFresh(deadline); err != nil {
			return contracts.AccountSession{}, err
		}
		if err := clock.Sleep(ctx, min(interval, deadline.Sub(clock.Now()))); err != nil {
			return contracts.AccountSession{}, err
		}
		if err := a.assertFresh(deadline); err != nil {
			return contracts.AccountSession{}, err
		}
		resp, err := a.post(ctx, "/user_management/authenticate", [][2]string{
			{"grant_type", "urn:ietf:params:oauth:grant-type:device_code"},
			{"device_code", challenge.DeviceCode},
		}, deadline.Sub(clock.Now()))
		if err != nil {
			return contracts.AccountSession{}, err
		}
		body, err := responseJSON(resp)
		if err != nil {
			return contracts.AccountSession{}, err
		}
		if err := a.assertFresh(deadline); err != nil {
			return contracts.AccountSession{}, err
		}
		if responseOK(resp) {
			return contracts.ParseDeviceSession(body)
		}
		if interval, err = nextInterval(body, interval); err != nil {
			return contracts.AccountSession{}, err
		}
	}
}

func (a *WorkosDeviceAuth) assertFresh(deadline time.Time) error {
	if !a.options.Clock.Now().Before(deadline) {
		return clierror.New("Account authorization expired. Run durin to sign in again.")
	}
	return nil
}

// post sends a form with client_id first, then fields in order, timing out after timeout
// (at most 15 seconds).
func (a *WorkosDeviceAuth) post(ctx context.Context, path string, fields [][2]string, timeout time.Duration) (*http.Response, error) {
	target, err := weburl.ParseRef(path, a.options.Origin)
	if err != nil {
		return nil, err
	}
	form := weburl.NewSearchParams(append([][2]string{{"client_id", a.options.ClientID}}, fields...)...).String()
	return fetch(ctx, a.options.Fetch, target.Href(), fetchInit{
		Method:  http.MethodPost,
		Header:  [][2]string{{"content-type", "application/x-www-form-urlencoded"}},
		Body:    &form,
		Timeout: max(min(timeout, providerRequestTimeout), time.Nanosecond),
	})
}
