// Package contracts holds the data the CLI exchanges with the durin app, WorkOS, the MCP
// gateway and agent harnesses, and the rules each value must satisfy. It mirrors
// src/contracts in the TypeScript CLI; validation functions live beside the types they check.
//
// Values that the CLI passes through or prints (tool catalogs, tool results) are kept as
// ordered JSON (jsjson) so their field order and number formatting stay exactly as before.
package contracts

import "github.com/accessdurin/cli/internal/jsjson"

// CliProfileScope is what a profile is bound to. Field order is the schema's: a profile's id
// is the SHA-256 of JSON.stringify(scope) in exactly this order.
type CliProfileScope struct {
	AppOrigin            string `json:"appOrigin"`
	AccountIssuer        string `json:"accountIssuer"`
	AccountClientID      string `json:"accountClientId"`
	PrincipalID          string `json:"principalId"`
	WorkosOrganizationID string `json:"workosOrganizationId"`
	OrganizationID       string `json:"organizationId"`
	Environment          string `json:"environment"` // sandbox | production
	Resource             string `json:"resource"`
	McpIssuer            string `json:"mcpIssuer"`
	Development          bool   `json:"development"`
}

// CliProfile is a saved organization profile: its scope, version 1 and id.
type CliProfile struct {
	CliProfileScope
	Version int    `json:"version"`
	ID      string `json:"id"`
}

// CliToolCall is one governed tool invocation.
type CliToolCall struct {
	Name           string
	Arguments      *jsjson.Object
	IdempotencyKey string
	ApprovalID     string // empty when absent
}

// CliToolCatalog is a validated tools/list result ({tools, ...}), kept as ordered JSON.
type CliToolCatalog = *jsjson.Object

// McpToolResult is a validated tools/call result, kept as ordered JSON.
type McpToolResult = *jsjson.Object

// AccountSession is a WorkOS account session (src/ports/cli-auth.ts).
type AccountSession struct {
	AccessToken    string
	RefreshToken   string
	PrincipalID    string
	OrganizationID *string // nil when the session has no organization
}

// AuthorizationPrompt is what the user sees to authorize this device.
type AuthorizationPrompt struct {
	Code string
	URL  string
}

// Organization is one verified membership.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// VerifiedOrganizations is the account identity the durin app verified.
type VerifiedOrganizations struct {
	PrincipalID    string
	OrganizationID string
	Organizations  []Organization
}

// OnboardingStep names an onboarding stage.
type OnboardingStep string

const (
	StepTitle        OnboardingStep = "title"
	StepHeadcount    OnboardingStep = "headcount"
	StepSource       OnboardingStep = "source"
	StepPayment      OnboardingStep = "payment"
	StepProvisioning OnboardingStep = "provisioning"
	StepComplete     OnboardingStep = "complete"
)

// OnboardingProfile is the organization creator's onboarding record.
type OnboardingProfile struct {
	OrganizationID string  `json:"organizationId"`
	CreatorID      string  `json:"creatorId"`
	Enrollment     string  `json:"enrollment"`
	Title          *string `json:"title"`
	Headcount      *int64  `json:"headcount"`
	Source         *string `json:"source"`
	OtherSource    *string `json:"otherSource"`
	PaymentReady   bool    `json:"paymentReady"`
	CompletedAt    *int64  `json:"completedAt"`
	Version        int64   `json:"version"`
}

// OnboardingStatus is GET /api/v1/onboarding.
type OnboardingStatus struct {
	OrganizationID *string            `json:"organizationId"`
	Journey        string             `json:"journey"`
	Next           string             `json:"next"`
	Allowed        bool               `json:"allowed"`
	Profile        *OnboardingProfile `json:"profile"`
	Step           OnboardingStep     `json:"step"`
	TenantReady    bool               `json:"tenantReady"`
}

// OnboardingAnswer answers one onboarding question. Exactly one of the value fields applies,
// by Step: TextValue for title, NumberValue for headcount, TextValue (+Other) for source.
type OnboardingAnswer struct {
	Step        OnboardingStep
	Version     int64
	TextValue   string
	NumberValue float64
	Other       string // source "other" detail; empty when absent
}

// HookFormat names a harness's hook configuration dialect.
type HookFormat string

// HookConfig is where a harness reads its hooks and in which dialect.
type HookConfig struct {
	Path   string     `json:"path"`
	Format HookFormat `json:"format"`
}

// AgentConfig describes how to configure one agent harness (src/agents.ts AgentConfig).
type AgentConfig struct {
	Name            string
	ConfigPath      string
	Format          string // jsonc | toml
	ServerKey       string
	URLKey          string
	Transport       *jsjson.Object // string | bool values
	Bridge          string         // arguments | command-array
	BridgeTransport *jsjson.Object // string | bool | []string values
	Authentication  string
	ID              string // empty when absent
	SkillsDirectory string // empty when absent
	HookConfig      *HookConfig
}

// HookChecker is a user-selected local pre-tool checker.
type HookChecker struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	TimeoutMs int      `json:"timeoutMs"`
}

// PendingCliCall is a bridge operation awaiting its outcome.
type PendingCliCall struct {
	IdempotencyKey string `json:"idempotencyKey"`
	ApprovalID     string `json:"approvalId,omitempty"`
}

// UnrevokedGrant is only what revoking a grant needs.
type UnrevokedGrant struct {
	ClientID     string
	AccessToken  string
	RefreshToken string // empty when absent
}
