package app

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// accountRequestTimeout bounds each durin API request, body included.
const accountRequestTimeout = 15 * time.Second

// AccountApiOptions configure a DurinAccountApi. AccessToken is read for every request;
// Refresh, when set, renews it once after a 401.
type AccountApiOptions struct {
	AppOrigin   string
	AccessToken func() string
	Fetch       *http.Client
	Refresh     func(ctx context.Context) error
}

// StaticToken is an AccessToken that never changes.
func StaticToken(token string) func() string { return func() string { return token } }

// AccountApiError is a failed durin API request: its HTTP status and the API's error code,
// or invalid_response when the body names none that is safe to show.
type AccountApiError struct {
	Status int
	Code   string
	err    *clierror.Error
}

func (e *AccountApiError) Error() string { return e.err.Message }

// Unwrap makes an AccountApiError an expected CLI error.
func (e *AccountApiError) Unwrap() error { return e.err }

func newAccountApiError(status int, code string) *AccountApiError {
	message := fmt.Sprintf("Durin request failed: %s (%d).", code, status)
	return &AccountApiError{Status: status, Code: code, err: &clierror.Error{Message: message}}
}

// safeErrorCode is /^[a-z_]{1,100}$/iu: with Unicode case folding, as in JavaScript, ſ and the
// Kelvin sign match too.
var safeErrorCode = regexp.MustCompile(`^(?i)[a-z_]{1,100}$`)

func responseError(status int, body jsjson.Value) *AccountApiError {
	code := "invalid_response"
	if parsed, issues := zod.SafeParse(contracts.ApiErrorSchema, body); issues == nil {
		e, _ := parsed.(*jsjson.Object).GetObject("error")
		code, _ = e.GetString("code")
	}
	if !safeErrorCode.MatchString(code) {
		code = "invalid_response"
	}
	return newAccountApiError(status, code)
}

// DurinSession is GET /api/v1/session (sessionSchema).
type DurinSession struct {
	Mode           string                   `json:"mode"`
	PrincipalID    string                   `json:"principalId"`
	Name           string                   `json:"name"`
	Email          string                   `json:"email"`
	EmailVerified  *bool                    `json:"emailVerified"`
	Role           string                   `json:"role"`
	OrganizationID string                   `json:"organizationId"`
	Organizations  []contracts.Organization `json:"organizations"`
	Onboarding     *OnboardingAccess        `json:"onboarding"`
}

// Verified is the session as the identity organization selection checks against.
func (s DurinSession) Verified() contracts.VerifiedOrganizations {
	return contracts.VerifiedOrganizations{PrincipalID: s.PrincipalID, OrganizationID: s.OrganizationID, Organizations: s.Organizations}
}

// OnboardingAccess is onboardingAccessSchema.
type OnboardingAccess struct {
	OrganizationID *string `json:"organizationId"`
	Journey        string  `json:"journey"`
	Next           string  `json:"next"`
	Allowed        bool    `json:"allowed"`
}

// McpEndpoint is mcpEndpointSchema: URL is empty when Status is unconfigured.
type McpEndpoint struct {
	Status string `json:"status"` // ready | needs_setup | unconfigured
	URL    string `json:"url"`
}

// PersonalBootstrap is GET /api/v1/me/bootstrap (personalBootstrapSchema).
type PersonalBootstrap struct {
	Organization struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Environment string `json:"environment"`
	} `json:"organization"`
	Membership struct {
		PrincipalID     string `json:"principalId"`
		Role            string `json:"role"`
		Active          bool   `json:"active"`
		RevocationEpoch int64  `json:"revocationEpoch"`
	} `json:"membership"`
	Entitlements struct {
		OrganizationID string `json:"organizationId"`
		Package        string `json:"package"`
	} `json:"entitlements"`
	Session  DurinSession `json:"session"`
	Mcp      McpEndpoint  `json:"mcp"`
	Settings struct {
		Retention   string `json:"retention"`
		Environment string `json:"environment"`
	} `json:"settings"`
}

// OrganizationCreated is organizationCreatedSchema.
type OrganizationCreated struct {
	OrganizationID       string `json:"organizationId"`
	WorkosOrganizationID string `json:"workosOrganizationId"`
	Status               string `json:"status"`
}

// OrganizationCreate is organizationCreateSchema's input.
type OrganizationCreate struct {
	Name           string
	IdempotencyKey string
}

// DurinAccountApi is the durin account API (src/account-api.ts). Every request carries the
// account's bearer token, the app's origin and x-requested-with: durin, never follows a
// redirect, and times out after 15 seconds.
type DurinAccountApi struct {
	options AccountApiOptions
}

var _ ports.OrganizationOnboardingApi = (*DurinAccountApi)(nil)

// NewDurinAccountApi returns the API for options.
func NewDurinAccountApi(options AccountApiOptions) *DurinAccountApi {
	return &DurinAccountApi{options: options}
}

// Session is GET /api/v1/session.
func (a *DurinAccountApi) Session(ctx context.Context) (DurinSession, error) {
	var out DurinSession
	return out, a.decode(ctx, http.MethodGet, "/v1/session", contracts.SessionSchema, nil, &out)
}

// Workspace is GET /api/v1/me/bootstrap.
func (a *DurinAccountApi) Workspace(ctx context.Context) (PersonalBootstrap, error) {
	var out PersonalBootstrap
	return out, a.decode(ctx, http.MethodGet, "/v1/me/bootstrap", contracts.PersonalBootstrapSchema, nil, &out)
}

// SetupEndpoint is POST /api/v1/mcp-endpoint.
func (a *DurinAccountApi) SetupEndpoint(ctx context.Context) (McpEndpoint, error) {
	var out McpEndpoint
	return out, a.decode(ctx, http.MethodPost, "/v1/mcp-endpoint", contracts.McpEndpointSchema, jsjson.NewObject(), &out)
}

// CreateOrganization is POST /api/v1/organizations.
func (a *DurinAccountApi) CreateOrganization(ctx context.Context, input OrganizationCreate) (OrganizationCreated, error) {
	var out OrganizationCreated
	body, err := zod.Parse(contracts.OrganizationCreateSchema, jsjson.Obj(
		jsjson.KV("name", jsjson.String(input.Name)),
		jsjson.KV("idempotencyKey", jsjson.String(input.IdempotencyKey)),
	))
	if err != nil {
		return out, err
	}
	return out, a.decode(ctx, http.MethodPost, "/v1/organizations", contracts.OrganizationCreatedSchema, body, &out)
}

// Status is GET /api/v1/onboarding.
func (a *DurinAccountApi) Status(ctx context.Context) (contracts.OnboardingStatus, error) {
	return a.onboarding(ctx, http.MethodGet, "/v1/onboarding", nil)
}

var okSchema = zod.Object(zod.F("ok", zod.Literal(jsjson.Bool(true))))

// Start is POST /api/v1/onboarding/start.
func (a *DurinAccountApi) Start(ctx context.Context) error {
	_, err := a.request(ctx, http.MethodPost, "/v1/onboarding/start", okSchema, jsjson.NewObject())
	return err
}

// Answer is PUT /api/v1/onboarding/answer.
func (a *DurinAccountApi) Answer(ctx context.Context, answer contracts.OnboardingAnswer) (contracts.OnboardingStatus, error) {
	body, err := zod.Parse(contracts.OnboardingAnswerSchema, answer.JSON())
	if err != nil {
		return contracts.OnboardingStatus{}, err
	}
	return a.onboarding(ctx, http.MethodPut, "/v1/onboarding/answer", body)
}

// Complete is POST /api/v1/onboarding/complete.
func (a *DurinAccountApi) Complete(ctx context.Context) (contracts.OnboardingStatus, error) {
	return a.onboarding(ctx, http.MethodPost, "/v1/onboarding/complete", jsjson.NewObject())
}

func (a *DurinAccountApi) onboarding(ctx context.Context, method, path string, body jsjson.Value) (contracts.OnboardingStatus, error) {
	result, err := a.request(ctx, method, path, contracts.OnboardingStatusSchema, body)
	if err != nil {
		return contracts.OnboardingStatus{}, err
	}
	var status contracts.OnboardingStatus
	return status, jsjson.Decode(result, &status)
}

func (a *DurinAccountApi) decode(ctx context.Context, method, path string, schema zod.Schema, body jsjson.Value, out any) error {
	result, err := a.request(ctx, method, path, schema, body)
	if err != nil {
		return err
	}
	return jsjson.Decode(result, out)
}

// request sends one call and parses its result with schema. A body that is not JSON fails
// before the status is considered, as `await response.json()` does.
func (a *DurinAccountApi) request(ctx context.Context, method, path string, schema zod.Schema, body jsjson.Value) (jsjson.Value, error) {
	resp, err := a.send(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	result, err := responseJSON(resp)
	if err != nil {
		return nil, err
	}
	if !responseOK(resp) {
		return nil, responseError(resp.StatusCode, result)
	}
	return zod.Parse(schema, result)
}

func (a *DurinAccountApi) send(ctx context.Context, method, path string, body jsjson.Value) (*http.Response, error) {
	resp, err := a.sendOnce(ctx, method, path, body)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || a.options.Refresh == nil {
		return resp, err
	}
	resp.Body.Close()
	if err := a.options.Refresh(ctx); err != nil {
		return nil, err
	}
	return a.sendOnce(ctx, method, path, body)
}

func (a *DurinAccountApi) sendOnce(ctx context.Context, method, path string, body jsjson.Value) (*http.Response, error) {
	target, err := weburl.ParseRef("/api"+path, a.options.AppOrigin)
	if err != nil {
		return nil, err
	}
	init := fetchInit{
		Method:  method,
		Timeout: accountRequestTimeout,
		Header: [][2]string{
			{"authorization", "Bearer " + a.options.AccessToken()},
			{"origin", a.options.AppOrigin},
			{"x-requested-with", "durin"},
			{"content-type", "application/json"},
		},
	}
	if body != nil {
		text := jsjson.Stringify(body)
		init.Body = &text
	}
	return fetch(ctx, a.options.Fetch, target.Href(), init)
}
