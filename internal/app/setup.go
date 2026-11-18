package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/credentials"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
)

// SetupOptions are what interactive setup needs (src/setup.ts). Intent is asked when empty;
// Request, Store, Clock and Agents default to the real ones.
type SetupOptions struct {
	AppOrigin   string
	Development bool
	Directory   string
	// ProgramPath is the executable installed as the agents' runtime.
	ProgramPath string
	UI          ports.SetupUi
	Intent      ports.AccountIntent
	Request     *http.Client
	Store       ports.CredentialStore
	Clock       ports.PollingClock
	Agents      map[string]contracts.AgentConfig
}

// setupContext is the options with every default filled in, Request restricted to the
// deployment's hosts.
type setupContext struct {
	SetupOptions
}

// RunSetup signs the account in, onboards its organization, then configures the chosen agent
// and signs its profile in to the organization's MCP server.
func RunSetup(ctx context.Context, options SetupOptions) error {
	setup, err := newSetupContext(ctx, options)
	if err != nil {
		return err
	}
	options.UI.Stage("Authenticate your Durin account")
	// SetupAccount guards its own requests: its hosts depend on the account issuer it learns.
	request := options.Request
	if request == nil {
		request = http.DefaultClient
	}
	account, err := SetupAccount(ctx, SetupAccountOptions{
		AppOrigin:   options.AppOrigin,
		Development: options.Development,
		Request:     request,
		Clock:       setup.Clock,
		UI:          options.UI,
		Intent:      setup.Intent,
	})
	if err != nil {
		return err
	}
	options.UI.Stage("Complete organization onboarding")
	if _, err := OnboardOrganization(ctx, OnboardingOptions{
		Api:       account.Api,
		Questions: setupOnboardingQuestions{options, account.Identity.OrganizationID},
		Clock:     setup.Clock,
	}); err != nil {
		return err
	}
	return setup.setupAgent(ctx, account)
}

// setupOnboardingQuestions opens the app's sign-in for the organization once payment setup
// is announced.
type setupOnboardingQuestions struct {
	options              SetupOptions
	workosOrganizationID string
}

func (q setupOnboardingQuestions) Answer(ctx context.Context, step contracts.OnboardingStep, version int64) (contracts.OnboardingAnswer, error) {
	return q.options.UI.Onboarding.Answer(ctx, step, version)
}

func (q setupOnboardingQuestions) Payment(ctx context.Context, organizationID string) error {
	if err := q.options.UI.Onboarding.Payment(ctx, organizationID); err != nil {
		return err
	}
	url, err := weburl.ParseRef("/auth/login", q.options.AppOrigin)
	if err != nil {
		return err
	}
	url.SetSearch(weburl.NewSearchParams([2]string{"provider", "authkit"}, [2]string{"organizationId", q.workosOrganizationID}).String())
	return q.options.UI.OpenAuthorization(ctx, url.Href())
}

func newSetupContext(ctx context.Context, options SetupOptions) (*setupContext, error) {
	if options.Intent == "" {
		intent, err := options.UI.Account.Intent(ctx)
		if err != nil {
			return nil, err
		}
		options.Intent = intent
	}
	allowlist, err := contracts.DeploymentHostAllowlist(contracts.CliHostScope{AppOrigin: options.AppOrigin, Development: options.Development})
	if err != nil {
		return nil, err
	}
	options.Request = SecureFetch(allowlist, options.Request)
	if options.Store == nil {
		options.Store = setupCredentialStore{credentials.NewNative("com.getdurin.cli")}
	}
	if options.Clock == nil {
		options.Clock = NewPollingClock()
	}
	return &setupContext{options}, nil
}

func (c *setupContext) setupAgent(ctx context.Context, account SetupAccountResult) error {
	workspace, err := account.Api.Workspace(ctx)
	if err != nil {
		return err
	}
	if err := assertWorkspace(account.Identity, workspace); err != nil {
		return err
	}
	agent, err := c.chosenAgent(ctx)
	if err != nil {
		return err
	}
	endpoint := workspace.Mcp
	if endpoint.Status == "needs_setup" {
		if endpoint, err = account.Api.SetupEndpoint(ctx); err != nil {
			return err
		}
	}
	if endpoint.Status != "ready" {
		return clierror.New("Personal MCP endpoint setup is unavailable. Ask your administrator to enable MCP.")
	}
	issuer, err := mcpIssuer(ctx, endpoint.URL, c.Request, contracts.CliHostScope{AppOrigin: c.AppOrigin, Development: c.Development})
	if err != nil {
		return err
	}
	profile, err := CreateProfile(contracts.CliProfileScope{
		AppOrigin:            account.Scope.AppOrigin,
		AccountIssuer:        account.Scope.AccountIssuer,
		AccountClientID:      account.Scope.AccountClientID,
		PrincipalID:          account.Scope.PrincipalID,
		WorkosOrganizationID: account.Identity.OrganizationID,
		OrganizationID:       workspace.Organization.ID,
		Environment:          workspace.Settings.Environment,
		Resource:             endpoint.URL,
		McpIssuer:            issuer,
		Development:          c.Development,
	})
	if err != nil {
		return err
	}
	profiles := NewFileProfiles(c.Directory)
	if err := profiles.Save(profile); err != nil {
		return err
	}
	executable, err := InstallRuntime(c.Directory, c.ProgramPath)
	if err != nil {
		return err
	}
	bridge := BridgeConfiguration{ExecutablePath: executable, ConfigDirectory: c.Directory, ProfileID: profile.ID}
	if err := WriteBridgeConfig(agent, bridge); err != nil {
		return err
	}
	c.UI.Agent.Configured(agent.ConfigPath)
	c.UI.Stage("Sign in to this organization’s Durin MCP")
	if _, err := SignInMcp(ctx, SignInMcpInput{Directory: c.Directory, Profile: profile, Store: c.Store, Request: c.Request, Open: c.UI.OpenAuthorization}); err != nil {
		return err
	}
	c.UI.Stage("Verify the saved agent bridge")
	toolCount, err := VerifySavedBridge(ctx, agent, bridge)
	if err != nil {
		return err
	}
	if err := c.setupIntegrations(ctx, agent, bridge); err != nil {
		return err
	}
	if err := profiles.Activate(profile.ID); err != nil {
		return err
	}
	c.UI.Agent.Ready(ports.SetupReady{
		Organization: workspace.Organization.Name,
		Agent:        agent.Name,
		ProfileID:    profile.ID,
		ToolCount:    toolCount,
	})
	return nil
}

// SignInMcpInput is what signing a profile in to its MCP server needs.
type SignInMcpInput struct {
	Directory string
	Profile   contracts.CliProfile
	Store     ports.CredentialStore
	Request   *http.Client
	Open      func(ctx context.Context, url string) error
}

// SignInMcp signs the profile in to its MCP server, first retrying revocations an earlier
// sign-in left, and returns how many tools its catalog lists.
func SignInMcp(ctx context.Context, input SignInMcpInput) (int, error) {
	if err := RetryPendingRevocations(ctx, input.Directory, input.Profile, input.Store, input.Request); err != nil {
		return 0, err
	}
	return WithProfileLock(input.Directory, input.Profile.ID, func() (int, error) {
		marker, err := NewFileRevocationMarker(input.Directory, input.Profile.ID)
		if err != nil {
			return 0, err
		}
		pending, err := NewFilePendingRevocations(input.Directory, input.Profile.ID, input.Store)
		if err != nil {
			return 0, err
		}
		return authorizeMcp(ctx, authorizeMcpOptions{
			Profile: input.Profile,
			Store:   input.Store,
			Marker:  marker,
			Pending: pending,
			Request: input.Request,
			Open:    input.Open,
		})
	}, DefaultLockPatience)
}

// authorizeMcpOptions are what authorizeMcp (src/setup-mcp.ts) needs.
type authorizeMcpOptions struct {
	Profile contracts.CliProfile
	Store   ports.CredentialStore
	Marker  ports.RevocationMarker
	Pending ports.PendingRevocations
	Request *http.Client
	Open    func(ctx context.Context, url string) error
}

func (c *setupContext) chosenAgent(ctx context.Context) (contracts.AgentConfig, error) {
	key, err := c.UI.Agent.Choose(ctx)
	if err != nil {
		return contracts.AgentConfig{}, err
	}
	if key == "custom" {
		return c.UI.Agent.Custom(ctx)
	}
	return c.standardAgent(key)
}

func (c *setupContext) standardAgent(key string) (contracts.AgentConfig, error) {
	agents := c.Agents
	if agents == nil {
		agents = LocalAgents()
	}
	agent, ok := agents[key]
	if !ok {
		return agent, clierror.New("Choose a supported agent during onboarding.")
	}
	if !AgentInstalled(key, ProcessEnv, currentPlatform()) {
		return agent, clierror.New("Install " + agent.Name + ", then run durin to resume. Your organization answers are saved.")
	}
	return agent, nil
}

func (c *setupContext) setupIntegrations(ctx context.Context, agent contracts.AgentConfig, bridge BridgeConfiguration) error {
	c.UI.Stage("Install Durin agent skills and local hooks")
	skills, err := InstallSkills(agent)
	if err != nil {
		return err
	}
	var checker *contracts.HookChecker
	if agent.HookConfig != nil {
		if checker, err = c.UI.Agent.Checker(ctx, agent); err != nil {
			return err
		}
	}
	if checker != nil {
		if err := InstallHooks(agent, bridge, *checker); err != nil {
			return err
		}
	}
	c.UI.Agent.Integrations(ports.IntegrationsResult{Skills: skills, Hooks: checker != nil, Custom: strings.HasPrefix(agent.ID, "custom")})
	return nil
}

func assertWorkspace(identity DurinSession, workspace PersonalBootstrap) error {
	matches := identity.PrincipalID == workspace.Session.PrincipalID &&
		identity.OrganizationID == workspace.Session.OrganizationID &&
		workspace.Membership.Active &&
		workspace.Membership.PrincipalID == identity.PrincipalID
	if !matches {
		return clierror.New("Workspace does not match your verified membership.")
	}
	return nil
}

// setupCredentialStore is NativeCredentialStore (src/credentials.ts): the OS credential store,
// whose failures are expected errors that say to unlock it.
type setupCredentialStore struct{ store credentials.Store }

func credentialStoreError(err error) error {
	if err == nil {
		return nil
	}
	return clierror.Wrap(credentials.UnavailableMessage, err)
}

func (s setupCredentialStore) Read(key string) (string, bool, error) {
	value, found, err := s.store.Read(key)
	return value, found, credentialStoreError(err)
}

func (s setupCredentialStore) Write(key, value string) error {
	return credentialStoreError(s.store.Write(key, value))
}

func (s setupCredentialStore) Remove(key string) error {
	return credentialStoreError(s.store.Remove(key))
}
