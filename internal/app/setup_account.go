package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// SetupAccountOptions are what signing in during setup needs. Request may be unguarded:
// SetupAccount restricts it to the deployment's hosts itself.
type SetupAccountOptions struct {
	AppOrigin   string
	Development bool
	Request     *http.Client
	Clock       ports.PollingClock
	UI          ports.SetupUi
	Intent      ports.AccountIntent
}

// AccountScope is the part of a profile's scope that sign-in establishes.
type AccountScope struct {
	AppOrigin       string
	AccountIssuer   string
	AccountClientID string
	PrincipalID     string
}

// SetupAccountResult is the signed-in account: the API, refreshing its token as needed, the
// identity it verified, and the profile scope so far.
type SetupAccountResult struct {
	Api      *DurinAccountApi
	Identity DurinSession
	Scope    AccountScope
}

// cliAuthConfig is cliAuthConfigSchema's output.
type cliAuthConfig struct {
	ClientID       string `json:"clientId"`
	Issuer         string `json:"issuer"`
	ProviderOrigin string `json:"providerOrigin"`
	AppOrigin      string `json:"appOrigin"`
}

// creationKey is a stable idempotency key for creating name: SHA-256 of
// JSON.stringify([appOrigin, principalId, name]) written as a version 5, RFC 4122 variant UUID.
func creationKey(session contracts.AccountSession, appOrigin, name string) string {
	sum := sha256.Sum256([]byte(jsjson.Stringify(jsjson.Strings(appOrigin, session.PrincipalID, name))))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-5" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func nativeConfiguration(ctx context.Context, options SetupAccountOptions) (cliAuthConfig, error) {
	var config cliAuthConfig
	target, err := weburl.ParseRef("/auth/cli/config", options.AppOrigin)
	if err != nil {
		return config, err
	}
	resp, err := fetch(ctx, options.Request, target.Href(), fetchInit{})
	if err != nil {
		return config, err
	}
	if !responseOK(resp) {
		resp.Body.Close()
		return config, clierror.New("Native account authentication is not configured on this Durin deployment. Ask its administrator to enable AuthKit CLI authentication.")
	}
	body, err := responseJSON(resp)
	if err != nil {
		return config, err
	}
	parsed, err := zod.Parse(contracts.CliAuthConfigSchema, body)
	if err != nil {
		return config, err
	}
	if err := jsjson.Decode(parsed, &config); err != nil {
		return config, err
	}
	trusted := config.AppOrigin == options.AppOrigin &&
		contracts.SafeCliURL(config.Issuer, options.Development) &&
		contracts.SafeCliURL(config.ProviderOrigin, options.Development)
	if !trusted {
		return config, clierror.New("Native authentication configuration does not match this deployment.")
	}
	if err := assertAccountHosts(config, options); err != nil {
		return config, err
	}
	config.Issuer, err = contracts.CanonicalIssuer(contracts.RoleAccountIssuer, config.Issuer, deploymentScope(options))
	return config, err
}

// assertAccountHosts fails before device sign-in when the account hosts aren't ones this
// deployment uses.
func assertAccountHosts(config cliAuthConfig, options SetupAccountOptions) error {
	account := accountHostScope(options, config)
	problem, err := contracts.UntrustedCliHost(contracts.RoleAccountIssuer, config.Issuer, account)
	if err == nil && problem == "" {
		problem, err = contracts.UntrustedCliHost(contracts.RoleAccountProvider, config.ProviderOrigin, account)
	}
	if err != nil {
		return err
	}
	if problem != "" {
		return clierror.New(problem + " Check --app-url, or ask your durin administrator.")
	}
	return nil
}

func deploymentScope(options SetupAccountOptions) contracts.CliHostScope {
	return contracts.CliHostScope{AppOrigin: options.AppOrigin, Development: options.Development}
}

func accountHostScope(options SetupAccountOptions, config cliAuthConfig) contracts.CliHostScope {
	return contracts.CliHostScope{AppOrigin: options.AppOrigin, Development: options.Development, AccountIssuer: config.Issuer}
}

func guarded(options SetupAccountOptions, scope contracts.CliHostScope) (SetupAccountOptions, error) {
	allowlist, err := contracts.DeploymentHostAllowlist(scope)
	if err != nil {
		return options, err
	}
	options.Request = SecureFetch(allowlist, options.Request)
	return options, nil
}

// SetupAccount signs the user in with the device flow, selects (or creates) the
// organization and verifies the account in it (src/setup-account.ts). Requests reach only
// the hosts the deployment uses, and api.workos.com for a self-hosted app only once its
// account issuer is known to be a WorkOS issuer.
func SetupAccount(ctx context.Context, unguarded SetupAccountOptions) (SetupAccountResult, error) {
	var result SetupAccountResult
	first, err := guarded(unguarded, deploymentScope(unguarded))
	if err != nil {
		return result, err
	}
	config, err := nativeConfiguration(ctx, first)
	if err != nil {
		return result, err
	}
	options, err := guarded(unguarded, accountHostScope(unguarded, config))
	if err != nil {
		return result, err
	}
	auth := NewWorkosDeviceAuth(DeviceAuthOptions{
		ClientID:    config.ClientID,
		Origin:      config.ProviderOrigin,
		Fetch:       options.Request,
		Clock:       options.Clock,
		Development: options.Development,
	})
	session, err := auth.Authorize(ctx, func(prompt contracts.AuthorizationPrompt) error {
		return options.UI.Account.Authorize(ctx, prompt, options.Intent)
	})
	if err != nil {
		return result, err
	}
	scope := AccountScope{
		AppOrigin:       options.AppOrigin,
		AccountIssuer:   config.Issuer,
		AccountClientID: config.ClientID,
		PrincipalID:     session.PrincipalID,
	}
	api := func(value contracts.AccountSession) *DurinAccountApi {
		return NewDurinAccountApi(AccountApiOptions{AppOrigin: options.AppOrigin, AccessToken: StaticToken(value.AccessToken), Fetch: options.Request})
	}
	identity, err := api(session).Session(ctx)
	if err != nil {
		return result, err
	}
	selected, err := SelectOrganization(ctx, ports.OrganizationSelection{
		Session:   session,
		Identity:  identity.Verified(),
		Questions: options.UI.Organizations,
		Refresh: func(ctx context.Context, previous contracts.AccountSession, organizationID string) (contracts.AccountSession, error) {
			return auth.Refresh(ctx, previous, &organizationID)
		},
		Create: func(ctx context.Context, name string) (string, error) {
			created, err := api(session).CreateOrganization(ctx, OrganizationCreate{Name: name, IdempotencyKey: creationKey(session, options.AppOrigin, name)})
			return created.WorkosOrganizationID, err
		},
	})
	if err != nil {
		return result, err
	}
	result, err = selectedAccount(ctx, options, auth, selected)
	result.Scope = scope
	return result, err
}

func selectedAccount(ctx context.Context, options SetupAccountOptions, auth ports.DeviceAccountAuth, session contracts.AccountSession) (SetupAccountResult, error) {
	selected := session
	api := NewDurinAccountApi(AccountApiOptions{
		AppOrigin:   options.AppOrigin,
		Fetch:       options.Request,
		AccessToken: func() string { return selected.AccessToken },
		Refresh: func(ctx context.Context) error {
			next, err := auth.Refresh(ctx, selected, nil)
			if err == nil {
				selected = next
			}
			return err
		},
	})
	verified, err := api.Session(ctx)
	if err != nil {
		return SetupAccountResult{}, err
	}
	if selected.OrganizationID == nil || verified.OrganizationID != *selected.OrganizationID || verified.PrincipalID != selected.PrincipalID {
		return SetupAccountResult{}, clierror.New("The verified account does not match the selected organization.")
	}
	return SetupAccountResult{Api: api, Identity: verified}, nil
}
