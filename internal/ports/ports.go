// Package ports declares the boundaries between the CLI's logic and the outside world: the
// OS credential store, saved profiles, the MCP gateway, WorkOS device sign-in, the durin
// account API and the interactive terminal. It is a one-to-one Go rendering of src/ports in
// the TypeScript CLI; implementations live elsewhere and tests substitute fakes.
package ports

import (
	"context"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
)

// CredentialStore is the OS credential store (src/ports/credential-store.ts). Read reports
// found=false for a missing entry; Remove of a missing entry succeeds.
type CredentialStore interface {
	Read(key string) (value string, found bool, err error)
	Write(key, value string) error
	Remove(key string) error
}

// RefusedProfile is a saved profile that no longer loads, with the reason it was refused.
type RefusedProfile struct {
	ID     string
	Reason string
}

// ProfileList is every saved profile: those that load, and those refused.
type ProfileList struct {
	Profiles []contracts.CliProfile
	Refused  []RefusedProfile
}

// CliProfiles stores organization profiles (src/ports/cli-profiles.ts). Load("") loads the
// active profile.
type CliProfiles interface {
	List() (ProfileList, error)
	Save(profile contracts.CliProfile) error
	Load(id string) (contracts.CliProfile, error)
	Activate(id string) error
}

// RevocationMarker records that logout kept a profile's grant because its revocation has not
// completed (src/ports/revocation-marker.ts).
type RevocationMarker interface {
	Pending() (bool, error)
	Mark() error
	Clear() error
}

// PendingRevocations are grants a new sign-in replaced before the authorization server
// confirmed their revocation; their tokens stay in the OS credential store
// (src/ports/pending-revocations.ts).
type PendingRevocations interface {
	Entries() ([]contracts.UnrevokedGrant, error)
	Add(grant contracts.UnrevokedGrant) error
	Replace(grants []contracts.UnrevokedGrant) error
}

// CliCallLedger persists bridge operation identifiers by request fingerprint, never tool
// arguments (src/ports/cli-call-ledger.ts). Read returns nil when nothing is pending.
type CliCallLedger interface {
	Read(fingerprint string) (*contracts.PendingCliCall, error)
	Save(fingerprint string, call contracts.PendingCliCall) error
	Remove(fingerprint string) error
}

// CliMcpAccess is an authenticated MCP session (src/ports/cli-mcp.ts).
type CliMcpAccess interface {
	Connect(ctx context.Context) error
	ListTools(ctx context.Context) (contracts.CliToolCatalog, error)
	CallTool(ctx context.Context, input contracts.CliToolCall) (contracts.McpToolResult, error)
	Close() error
}

// DeviceAccountAuth signs an account in with the OAuth device flow (src/ports/cli-auth.ts).
// A nil organizationID in Refresh keeps the session's organization.
type DeviceAccountAuth interface {
	Authorize(ctx context.Context, present func(contracts.AuthorizationPrompt) error) (contracts.AccountSession, error)
	Refresh(ctx context.Context, session contracts.AccountSession, organizationID *string) (contracts.AccountSession, error)
}

// PollingClock lets polling loops be tested without waiting (src/ports/cli-auth.ts).
type PollingClock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// OrganizationOnboardingApi is the durin onboarding API (src/ports/cli-onboarding.ts).
type OrganizationOnboardingApi interface {
	Status(ctx context.Context) (contracts.OnboardingStatus, error)
	Start(ctx context.Context) error
	Answer(ctx context.Context, answer contracts.OnboardingAnswer) (contracts.OnboardingStatus, error)
	Complete(ctx context.Context) (contracts.OnboardingStatus, error)
}

// OrganizationQuestions asks the organization creator's onboarding questions.
type OrganizationQuestions interface {
	Answer(ctx context.Context, step contracts.OnboardingStep, version int64) (contracts.OnboardingAnswer, error)
	Payment(ctx context.Context, organizationID string) error
}

// OrganizationSelectionQuestions chooses or creates the organization (src/ports/cli-organization.ts).
type OrganizationSelectionQuestions interface {
	Choose(ctx context.Context, organizations []contracts.Organization) (string, error)
	Create(ctx context.Context) (string, error)
}

// OrganizationSelection is everything selecting an organization needs.
type OrganizationSelection struct {
	Session   contracts.AccountSession
	Identity  contracts.VerifiedOrganizations
	Questions OrganizationSelectionQuestions
	Refresh   func(ctx context.Context, session contracts.AccountSession, organizationID string) (contracts.AccountSession, error)
	Create    func(ctx context.Context, name string) (workosOrganizationID string, err error)
}

// AccountIntent is login or signup.
type AccountIntent string

const (
	IntentLogin  AccountIntent = "login"
	IntentSignup AccountIntent = "signup"
)

// SetupAccountQuestions asks how to sign in (src/ports/cli-setup.ts).
type SetupAccountQuestions interface {
	Intent(ctx context.Context) (AccountIntent, error)
	Authorize(ctx context.Context, prompt contracts.AuthorizationPrompt, intent AccountIntent) error
}

// IntegrationsResult reports what setup installed besides the MCP bridge.
type IntegrationsResult struct {
	Skills  int
	Hooks   bool
	Checker bool // a local pre-tool checker is configured
	Custom  bool
	// Observer describes the observer service setup installed (empty when not attempted).
	Observer string
	// Reporting is true when calls to local MCP servers are reported to the organization.
	Reporting bool
}

// SetupReady is the summary setup shows when it finishes.
type SetupReady struct {
	Organization string
	Agent        string
	ProfileID    string
	ToolCount    int
}

// SetupAgentQuestions chooses and reports on the agent harness. Checker returns nil when the
// user skips local hook setup.
type SetupAgentQuestions interface {
	Choose(ctx context.Context) (string, error)
	Custom(ctx context.Context) (contracts.AgentConfig, error)
	Checker(ctx context.Context, agent contracts.AgentConfig) (*contracts.HookChecker, error)
	// Reporting asks whether to report calls to local MCP servers, with their arguments, to
	// the organization. endpoint is true when Durin Endpoint's helper is installed.
	Reporting(ctx context.Context, agent contracts.AgentConfig, endpoint bool) (bool, error)
	// Observer asks whether to install the observer service, which keeps a local capture of
	// full hook payloads (prompts, tool inputs and outputs). The default is no.
	Observer(ctx context.Context) (bool, error)
	Integrations(result IntegrationsResult)
	Configured(path string)
	Ready(result SetupReady)
}

// SetupUi is the interactive terminal (src/ports/cli-setup.ts).
type SetupUi struct {
	Account           SetupAccountQuestions
	Organizations     OrganizationSelectionQuestions
	Onboarding        OrganizationQuestions
	Agent             SetupAgentQuestions
	OpenAuthorization func(ctx context.Context, url string) error
	Stage             func(message string)
}
