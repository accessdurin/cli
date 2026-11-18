package app

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
)

// fakeClock advances only when slept on, recording each sleep.
type fakeClock struct {
	now     time.Time
	sleeps  []time.Duration
	onSleep func()
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	if c.onSleep != nil {
		c.onSleep()
	}
	return nil
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

const deviceTokens = `{"access_token":"private-access-token","refresh_token":"private-refresh-token","organization_id":"org_acme","user":{"id":"user_admin"}}`

func deviceChallengeJSON(expires int, complete string) string {
	challenge := map[string]any{
		"device_code": "private-device-code", "user_code": "ABCD-EFGH",
		"verification_uri": "https://auth.example/device", "verification_uri_complete": complete,
		"expires_in": expires, "interval": 5,
	}
	data, _ := json.Marshal(challenge)
	return string(data)
}

// deviceFixture answers the challenge, then each of replies in turn (tokens with 200, any
// other reply with 400).
func deviceFixture(replies []string, expires int) (*WorkosDeviceAuth, *fakeClock, func() []recorded, *[]string) {
	queue := append([]string{deviceChallengeJSON(expires, "https://auth.example/device?user_code=ABCD-EFGH")}, replies...)
	first := true
	client, requests := fakeClient(func(recorded) (int, string) {
		body := queue[0]
		queue = queue[1:]
		status := 400
		if first || body == deviceTokens {
			status = 200
		}
		first = false
		return status, body
	})
	clock := newFakeClock()
	auth := NewWorkosDeviceAuth(DeviceAuthOptions{ClientID: "client_public", Origin: "https://api.workos.com", Fetch: client, Clock: clock})
	return auth, clock, requests, &queue
}

func formOf(t *testing.T, body string) url.Values {
	t.Helper()
	values, err := url.ParseQuery(body)
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestDeviceAuthRefreshSwitchesOrganizationWithAPublicClient(t *testing.T) {
	client, requests := fakeClient(func(recorded) (int, string) {
		return 200, `{"access_token":"a","refresh_token":"r","organization_id":"org_selected","user":{"id":"user_admin"}}`
	})
	auth := NewWorkosDeviceAuth(DeviceAuthOptions{ClientID: "client_public", Origin: "https://api.workos.com", Fetch: client, Clock: newFakeClock()})
	acme := "org_acme"
	session := contracts.AccountSession{AccessToken: "old-access", RefreshToken: "old-refresh", PrincipalID: "user_admin", OrganizationID: &acme}
	selected := "org_selected"
	next, err := auth.Refresh(context.Background(), session, &selected)
	if err != nil || next.AccessToken != "a" || next.RefreshToken != "r" || *next.OrganizationID != "org_selected" {
		t.Fatalf("%+v %v", next, err)
	}
	r := requests()[0]
	if r.URL != "https://api.workos.com/user_management/authenticate" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("%+v", r)
	}
	// Fields in URLSearchParams order, and no client secret.
	if r.Body != "client_id=client_public&grant_type=refresh_token&refresh_token=old-refresh&organization_id=org_selected" {
		t.Errorf("body %s", r.Body)
	}
	// Refreshing into another organization, or as another user, is refused.
	if _, err := auth.Refresh(context.Background(), session, nil); err == nil {
		t.Fatal("a session that lands in another organization must be refused")
	} else {
		wantCliError(t, err, "Refreshed account does not match the selected profile.")
	}
}

func TestDeviceAuthWaitsForApproval(t *testing.T) {
	auth, clock, requests, _ := deviceFixture([]string{`{"error":"authorization_pending"}`, deviceTokens}, 300)
	var presented []contracts.AuthorizationPrompt
	session, err := auth.Authorize(context.Background(), func(p contracts.AuthorizationPrompt) error {
		presented = append(presented, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	org := "org_acme"
	want := contracts.AccountSession{AccessToken: "private-access-token", RefreshToken: "private-refresh-token", PrincipalID: "user_admin", OrganizationID: &org}
	if !reflect.DeepEqual(session, want) {
		t.Errorf("session %+v", session)
	}
	if !reflect.DeepEqual(presented, []contracts.AuthorizationPrompt{{Code: "ABCD-EFGH", URL: "https://auth.example/device?user_code=ABCD-EFGH"}}) {
		t.Errorf("presented %+v", presented)
	}
	if !reflect.DeepEqual(clock.sleeps, []time.Duration{5 * time.Second, 5 * time.Second}) {
		t.Errorf("sleeps %v", clock.sleeps)
	}
	got := requests()
	poll := "client_id=client_public&grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&device_code=private-device-code"
	if len(got) != 3 || got[0].Body != "client_id=client_public" || got[1].Body != poll || got[2].Body != poll {
		t.Errorf("requests %+v", got)
	}
	if got[0].URL != "https://api.workos.com/user_management/authorize/device" {
		t.Errorf("url %s", got[0].URL)
	}
}

func TestDeviceAuthPolling(t *testing.T) {
	present := func(contracts.AuthorizationPrompt) error { return nil }

	// slow_down lengthens every later poll.
	auth, clock, _, _ := deviceFixture([]string{`{"error":"slow_down"}`, `{"error":"authorization_pending"}`, deviceTokens}, 300)
	if _, err := auth.Authorize(context.Background(), present); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clock.sleeps, []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}) {
		t.Errorf("sleeps %v", clock.sleeps)
	}

	// No token is accepted once the challenge has expired.
	auth, _, _, queue := deviceFixture([]string{`{"error":"authorization_pending"}`, deviceTokens}, 10)
	_, err := auth.Authorize(context.Background(), present)
	wantCliError(t, err, "Account authorization expired. Run durin to sign in again.")
	if !reflect.DeepEqual(*queue, []string{deviceTokens}) {
		t.Errorf("unexpected requests; left %v", *queue)
	}

	// Any other error stops polling, without showing the provider's detail.
	for _, code := range []string{"access_denied", "expired_token", "invalid_client", "constructor"} {
		auth, _, _, queue := deviceFixture([]string{`{"error":"` + code + `","error_description":"private-provider-detail"}`, deviceTokens}, 300)
		_, err := auth.Authorize(context.Background(), present)
		wantCliError(t, err, "Account authorization failed.")
		if len(*queue) != 1 {
			t.Errorf("%s: kept polling", code)
		}
	}
}

func TestDeviceAuthRefusesUnsafeBrowserURLs(t *testing.T) {
	for _, unsafe := range []string{"javascript:alert(1)", "http://auth.example/device", "https://user:pass@auth.example/device", "https://auth.example/device#x"} {
		client, _ := fakeClient(func(recorded) (int, string) { return 200, deviceChallengeJSON(300, unsafe) })
		auth := NewWorkosDeviceAuth(DeviceAuthOptions{ClientID: "client_public", Origin: "https://api.workos.com", Fetch: client, Clock: newFakeClock()})
		presented := false
		_, err := auth.Authorize(context.Background(), func(contracts.AuthorizationPrompt) error { presented = true; return nil })
		wantCliError(t, err, "Unsafe account authorization URL.")
		if presented {
			t.Errorf("%s was presented", unsafe)
		}
	}
	// Loopback HTTP is allowed in development, and its query is kept.
	client, _ := fakeClient(func(recorded) (int, string) {
		return 200, deviceChallengeJSON(300, "http://127.0.0.1:4567/device?user_code=ABCD-EFGH")
	})
	auth := NewWorkosDeviceAuth(DeviceAuthOptions{ClientID: "c", Origin: "http://127.0.0.1:4567", Fetch: client, Clock: newFakeClock(), Development: true})
	var shown string
	_, _ = auth.Authorize(context.Background(), func(p contracts.AuthorizationPrompt) error {
		shown = p.URL
		return context.Canceled
	})
	if shown != "http://127.0.0.1:4567/device?user_code=ABCD-EFGH" {
		t.Errorf("shown %q", shown)
	}
}
