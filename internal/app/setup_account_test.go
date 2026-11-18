package app

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/ports"
)

type noAccountQuestions struct{}

func (noAccountQuestions) Intent(context.Context) (ports.AccountIntent, error) {
	return ports.IntentLogin, nil
}

func (noAccountQuestions) Authorize(context.Context, contracts.AuthorizationPrompt, ports.AccountIntent) error {
	return nil
}

// signIn runs SetupAccount against a deployment whose config endpoint returns issuer and
// providerOrigin and whose other endpoints fail, recording each host and path contacted.
func signIn(t *testing.T, appOrigin, issuer, providerOrigin string, development bool) (string, []string) {
	t.Helper()
	config, _ := json.Marshal(map[string]string{"clientId": "client_account", "appOrigin": appOrigin, "issuer": issuer, "providerOrigin": providerOrigin})
	raw, requests := fakeClient(func(r recorded) (int, string) {
		if strings.HasSuffix(r.URL, "/auth/cli/config") {
			return 200, string(config)
		}
		return 503, "null"
	})
	allowlist, err := contracts.DeploymentHostAllowlist(contracts.CliHostScope{AppOrigin: appOrigin, Development: development})
	if err != nil {
		t.Fatal(err)
	}
	_, err = SetupAccount(context.Background(), SetupAccountOptions{
		AppOrigin:   appOrigin,
		Development: development,
		Request:     SecureFetch(allowlist, raw),
		Clock:       newFakeClock(),
		UI:          ports.SetupUi{Account: noAccountQuestions{}},
		Intent:      ports.IntentLogin,
	})
	var requested []string
	for _, r := range requests() {
		u, _ := url.Parse(r.URL)
		requested = append(requested, u.Host+u.Path)
	}
	if err == nil {
		t.Fatal("sign-in cannot succeed against this deployment")
	}
	return err.Error(), requested
}

const (
	production = "https://app.getdurin.com"
	staging    = "https://moria.app.getdurin.com"
	workos     = "https://api.workos.com"
)

func TestSetupAccountStartsDeviceSignInOnTheDeploymentsAccountHosts(t *testing.T) {
	cases := []struct{ app, issuer, provider, host string }{
		{production, "https://auth.getdurin.com/", workos, "api.workos.com"},
		{staging, "https://resilient-tiger-64-staging.authkit.app/", workos, "api.workos.com"},
		{"https://durin.acme.test", "https://durin.acme.test/", "https://durin.acme.test", "durin.acme.test"},
	}
	for _, c := range cases {
		outcome, requested := signIn(t, c.app, c.issuer, c.provider, false)
		appHost, _ := url.Parse(c.app)
		want := []string{appHost.Host + "/auth/cli/config", c.host + "/user_management/authorize/device"}
		if !strings.Contains(outcome, "Unable to start account sign-in.") || !reflect.DeepEqual(requested, want) {
			t.Errorf("%s: %q %v", c.app, outcome, requested)
		}
	}
	app := "http://127.0.0.1:4567"
	outcome, requested := signIn(t, app, app+"/", app, true)
	if !strings.Contains(outcome, "Unable to start account sign-in.") || requested[len(requested)-1] != "127.0.0.1:4567/user_management/authorize/device" {
		t.Errorf("development: %q %v", outcome, requested)
	}
	// A self-hosted app without a WorkOS issuer gets only its own host for the account API.
	_, requested = signIn(t, "https://durin.acme.test", "https://durin.acme.test/", workos, false)
	if !reflect.DeepEqual(requested, []string{"durin.acme.test/auth/cli/config"}) {
		t.Errorf("self-hosted: %v", requested)
	}
}

func TestSetupAccountNeverContactsAnUntrustedAccountHost(t *testing.T) {
	for _, provider := range []string{
		"https://api.workos.com.evil.example", "https://evil-workos.com", "https://workos.com",
		"https://attacker.api.workos.com", "https://app.getdurin.com.evil.example", "https://127.0.0.1:4567",
	} {
		outcome, requested := signIn(t, production, "https://auth.getdurin.com/", provider, false)
		host, _ := url.Parse(provider)
		if !reflect.DeepEqual(requested, []string{"app.getdurin.com/auth/cli/config"}) ||
			!strings.Contains(outcome, "The account sign-in service is on "+host.Hostname()) ||
			!strings.Contains(outcome, "Check --app-url, or ask your durin administrator.") || strings.Contains(outcome, "Durin") {
			t.Errorf("%s: %q %v", provider, outcome, requested)
		}
	}
	for _, c := range [][2]string{
		{production, "https://api.workos.com.evil.example/"},
		{production, "https://resilient-tiger-64-staging.authkit.app/"},
		{staging, "https://other-tenant.authkit.app/"},
		{staging, "https://auth.getdurin.com/"},
	} {
		outcome, requested := signIn(t, c[0], c[1], workos, false)
		app, _ := url.Parse(c[0])
		issuer, _ := url.Parse(c[1])
		if !reflect.DeepEqual(requested, []string{app.Host + "/auth/cli/config"}) || !strings.Contains(outcome, "The account sign-in issuer is on "+issuer.Hostname()) {
			t.Errorf("%v: %q %v", c, outcome, requested)
		}
	}
}

func TestCreationKeyIsTheTypeScriptUUID(t *testing.T) {
	session := contracts.AccountSession{PrincipalID: "user_01"}
	// Expected values computed by src/setup-account.ts creationKey; the second name has a line
	// separator and a lone surrogate, which JSON.stringify escapes.
	if got := creationKey(session, production, "Acme"); got != "314b1a21-beda-519c-a3ec-b6dcb9f7cedf" {
		t.Errorf("got %s", got)
	}
	if got := creationKey(session, production, " Acme   Corp \xed\xa0\x80"); got != "5f3b2727-ab5b-5a3a-a698-c94a03c06e5d" {
		t.Errorf("got %s", got)
	}
}

type fixedOrganizationQuestions struct {
	choice, name string
}

func (q fixedOrganizationQuestions) Choose(context.Context, []contracts.Organization) (string, error) {
	return q.choice, nil
}

func (q fixedOrganizationQuestions) Create(context.Context) (string, error) { return q.name, nil }

func TestSelectOrganization(t *testing.T) {
	session := contracts.AccountSession{AccessToken: "initial", RefreshToken: "refresh", PrincipalID: "user_one"}
	identity := contracts.VerifiedOrganizations{PrincipalID: "user_one", OrganizationID: "unassigned",
		Organizations: []contracts.Organization{{ID: "org_one", Name: "One"}, {ID: "org_two", Name: "Two"}}}
	var refreshed []string
	selection := ports.OrganizationSelection{
		Session:   session,
		Identity:  identity,
		Questions: fixedOrganizationQuestions{choice: "org_two"},
		Refresh: func(_ context.Context, previous contracts.AccountSession, organizationID string) (contracts.AccountSession, error) {
			refreshed = append(refreshed, organizationID)
			previous.AccessToken, previous.OrganizationID = "scoped", &organizationID
			return previous, nil
		},
		Create: func(context.Context, string) (string, error) {
			t.Fatal("unexpected creation")
			return "", nil
		},
	}
	result, err := SelectOrganization(context.Background(), selection)
	if err != nil || *result.OrganizationID != "org_two" || !reflect.DeepEqual(refreshed, []string{"org_two"}) {
		t.Fatalf("%+v %v %v", result, err, refreshed)
	}

	// Only verified memberships can be selected.
	selection.Questions = fixedOrganizationQuestions{choice: "org_other"}
	refreshed = nil
	_, err = SelectOrganization(context.Background(), selection)
	wantCliError(t, err, "verified membership")
	if refreshed != nil {
		t.Error("refreshed into an unverified organization")
	}

	// Without memberships, the named organization is created and selected.
	selection.Identity.Organizations = nil
	selection.Questions = fixedOrganizationQuestions{name: "Acme"}
	selection.Create = func(_ context.Context, name string) (string, error) { return "org_wos_" + name, nil }
	if result, err = SelectOrganization(context.Background(), selection); err != nil || *result.OrganizationID != "org_wos_Acme" {
		t.Fatalf("%+v %v", result, err)
	}

	// A refresh that lands elsewhere, or a changed identity, is refused.
	selection.Refresh = func(_ context.Context, previous contracts.AccountSession, _ string) (contracts.AccountSession, error) {
		return previous, nil
	}
	_, err = SelectOrganization(context.Background(), selection)
	wantCliError(t, err, "Account does not match the selected organization.")
	selection.Identity.PrincipalID = "user_two"
	_, err = SelectOrganization(context.Background(), selection)
	wantCliError(t, err, "Account identity changed.")
}
