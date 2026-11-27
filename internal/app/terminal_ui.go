package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// terminalUI is the interactive setup terminal (src/terminal-ui.ts). Every server-supplied
// name or code it shows passes through TerminalText.
type terminalUI struct {
	p    *prompter
	open func(ctx context.Context, url string) error
}

// TerminalUI is terminalUi(): setup's questions and messages on an interactive terminal.
func TerminalUI(in io.Reader, out io.Writer) ports.SetupUi {
	ui := &terminalUI{p: newPrompter(in, out), open: OpenBrowser}
	return ports.SetupUi{
		Account:           accountQuestions{ui},
		Organizations:     organizationQuestions{ui},
		Onboarding:        onboardingQuestions{ui},
		Agent:             agentQuestions{ui},
		OpenAuthorization: ui.openAuthorization,
		Stage:             ui.p.step,
	}
}

func (ui *terminalUI) openAuthorization(ctx context.Context, url string) error {
	ui.p.note(TerminalText(url), "Continue in your browser; this terminal will wait")
	return ui.open(ctx, url)
}

// fileQuestion asks for a path; the answer is returned as typed.
func (ui *terminalUI) fileQuestion(ctx context.Context, message string) (string, error) {
	return ui.p.text(ctx, message, func(value string) string {
		if jsTrim(value) == "" {
			return "Enter a file path."
		}
		return ""
	})
}

type accountQuestions struct{ *terminalUI }

func (q accountQuestions) Intent(ctx context.Context) (ports.AccountIntent, error) {
	value, err := q.p.selectOne(ctx, "Log in or create an account?", []promptOption{
		{Value: string(ports.IntentLogin), Label: "Log in"},
		{Value: string(ports.IntentSignup), Label: "Create an account"},
	})
	return ports.AccountIntent(value), err
}

func (q accountQuestions) Authorize(ctx context.Context, prompt contracts.AuthorizationPrompt, intent ports.AccountIntent) error {
	action := "Sign in and authorize this device."
	if intent == ports.IntentSignup {
		action = "Choose Create an account in the browser, then authorize this device."
	}
	q.p.note(action+"\nDevice code: "+TerminalText(prompt.Code), "Account authorization")
	return q.openAuthorization(ctx, prompt.URL)
}

type organizationQuestions struct{ *terminalUI }

func (q organizationQuestions) Choose(ctx context.Context, organizations []contracts.Organization) (string, error) {
	options := make([]promptOption, len(organizations))
	for i, organization := range organizations {
		options[i] = promptOption{Value: organization.ID, Label: TerminalText(organization.Name)}
	}
	return q.p.selectOne(ctx, "Which organization should this agent use?", options)
}

// Create asks for the new organization's name, returned as typed (the server trims nothing,
// and the creation key covers exactly what is sent).
func (q organizationQuestions) Create(ctx context.Context) (string, error) {
	return q.p.text(ctx, "What is your organization called?", func(value string) string {
		name := jsjson.Obj(jsjson.KV("name", jsjson.String(jsTrim(value))), jsjson.KV("idempotencyKey", jsjson.String(randomUUID())))
		if !zod.Valid(contracts.OrganizationCreateSchema, name) {
			return "Enter an organization name of 2–80 characters."
		}
		return ""
	})
}

type onboardingQuestions struct{ *terminalUI }

func (q onboardingQuestions) Answer(ctx context.Context, step contracts.OnboardingStep, version int64) (contracts.OnboardingAnswer, error) {
	if step == contracts.StepSource {
		return q.source(ctx, version)
	}
	return q.textAnswer(ctx, step, version)
}

// textAnswer asks the title or headcount question: the title is trimmed, the headcount read
// as Number() reads it.
func (q onboardingQuestions) textAnswer(ctx context.Context, step contracts.OnboardingStep, version int64) (contracts.OnboardingAnswer, error) {
	message := "What is your role or job title?"
	answer := func(value string) contracts.OnboardingAnswer {
		return contracts.OnboardingAnswer{Step: step, Version: version, TextValue: jsTrim(value)}
	}
	if step == contracts.StepHeadcount {
		message = "How many people work at your organization?"
		answer = func(value string) contracts.OnboardingAnswer {
			return contracts.OnboardingAnswer{Step: step, Version: version, NumberValue: jsNumber(value)}
		}
	} else if step != contracts.StepTitle {
		return contracts.OnboardingAnswer{}, fmt.Errorf("no question for onboarding step %q", step)
	}
	input, err := q.p.text(ctx, message, func(value string) string {
		if _, err := ParseOnboardingAnswer(answer(value)); err != nil {
			return "Enter a valid answer."
		}
		return ""
	})
	if err != nil {
		return contracts.OnboardingAnswer{}, err
	}
	return ParseOnboardingAnswer(answer(input))
}

func (q onboardingQuestions) source(ctx context.Context, version int64) (contracts.OnboardingAnswer, error) {
	options := make([]promptOption, len(contracts.DiscoverySources))
	for i, source := range contracts.DiscoverySources {
		options[i] = promptOption{Value: source.Value, Label: source.Label}
	}
	value, err := q.p.selectOne(ctx, "Where did you hear about Durin?", options)
	if err != nil {
		return contracts.OnboardingAnswer{}, err
	}
	other := ""
	if value == "other" {
		other, err = q.p.text(ctx, "Where did you hear about us?", func(detail string) string {
			trimmed := jsTrim(detail)
			if trimmed == "" || jsjson.UTF16Len(trimmed) > 240 {
				return "Enter 1–240 characters."
			}
			return ""
		})
		if err != nil {
			return contracts.OnboardingAnswer{}, err
		}
	}
	return ParseOnboardingAnswer(contracts.OnboardingAnswer{Step: contracts.StepSource, Version: version, TextValue: value, Other: other})
}

func (q onboardingQuestions) Payment(_ context.Context, organizationID string) error {
	q.p.info("Complete payment setup for " + TerminalText(organizationID) + " in the browser. Waiting for verified payment and provisioning.")
	return nil
}

type agentQuestions struct{ *terminalUI }

func (q agentQuestions) Choose(ctx context.Context) (string, error) {
	agents := AgentOptions()
	options := make([]promptOption, len(agents))
	for i, agent := range agents {
		options[i] = promptOption{Value: agent.Value, Label: agent.Label, Hint: agent.Hint}
	}
	return q.p.selectOne(ctx, "Which agent would you like to use?", options)
}

func (q agentQuestions) Custom(ctx context.Context) (contracts.AgentConfig, error) {
	path, err := q.fileQuestion(ctx, "Path to your custom harness descriptor JSON")
	if err != nil {
		return contracts.AgentConfig{}, err
	}
	return ReadCustomHarness(path)
}

// Checker offers the installed Durin Endpoint helper (when there is one for this agent), a
// custom checker descriptor, or skipping local hooks.
func (q agentQuestions) Checker(ctx context.Context, agent contracts.AgentConfig) (*contracts.HookChecker, error) {
	endpoint := EndpointChecker(agent.ID)
	var options []promptOption
	if endpoint != nil {
		options = append(options, promptOption{Value: "endpoint", Label: "Use the installed Durin Endpoint helper"})
	}
	options = append(options,
		promptOption{Value: "custom", Label: "Use a custom local checker", Hint: "Select a checker descriptor JSON file"},
		promptOption{Value: "skip", Label: "Skip local pre-tool checks", Hint: "Hooks still capture events locally; an existing checker is kept"},
	)
	choice, err := q.p.selectOne(ctx, "Configure local pre-tool checks?", options)
	if err != nil {
		return nil, err
	}
	switch choice {
	case "endpoint":
		return endpoint, nil
	case "custom":
		path, err := q.fileQuestion(ctx, "Path to your local checker descriptor JSON")
		if err != nil {
			return nil, err
		}
		checker, err := ReadChecker(path)
		if err != nil {
			return nil, err
		}
		return &checker, nil
	}
	return nil, nil
}

// Reporting explains what reporting local MCP tool calls sends and asks whether to turn it on.
func (q agentQuestions) Reporting(ctx context.Context, agent contracts.AgentConfig, endpoint bool) (bool, error) {
	takeover := "If you later install Durin Endpoint, it takes over and this hook stays quiet."
	if endpoint {
		takeover = "When Durin Endpoint’s helper runs for this agent, Endpoint reports these calls and this hook stays quiet."
	}
	q.p.note(strings.Join([]string{
		"When " + TerminalText(agent.Name) + " calls an MCP server running on this computer (a local command or a localhost URL),",
		"Durin sends the server, tool, outcome and tool arguments to your organization.",
		"The arguments are sent in full (up to 16 KiB); your organization’s capture policy decides on Durin’s side whether they are kept. Results are never sent.",
		takeover,
	}, "\n"), "Local MCP tool calls")
	return q.p.confirm(ctx, "Report local MCP tool calls to your organization?")
}

// Observer explains what the observer service keeps and asks whether to install it.
func (q agentQuestions) Observer(ctx context.Context) (bool, error) {
	q.p.note(strings.Join([]string{
		"The Durin observer is a background service that starts when you log in. It records the hook",
		"events your agents send, including prompts, tool inputs and outputs, which may contain secrets",
		"and source code, and connections to AI providers. The capture stays on this computer, encrypted,",
		"for up to 7 days or 512 MiB. Hooks work without it; durin observer install adds it later.",
	}, "\n"), "Durin observer")
	return q.p.confirm(ctx, "Install the Durin observer?")
}

func (q agentQuestions) Integrations(result ports.IntegrationsResult) {
	if result.Reporting {
		q.p.info("Local MCP tool calls: reporting configured; reopen the agent to activate.")
	}
	hooks := "not configured in this run; existing hooks retained"
	if result.Hooks {
		hooks = "configured; reopen the agent to activate"
	}
	q.p.info(fmt.Sprintf("Durin skills installed: %d. Local hooks: %s.", result.Skills, hooks))
	if result.Hooks && !result.Checker {
		q.p.info("Local pre-tool checks are off; hooks only capture events for the local observer.")
	}
	if result.Observer != "" {
		q.p.info("Durin observer: " + TerminalText(result.Observer) + ".")
	}
	if result.Custom && result.Hooks {
		q.p.info("Wire the generated hook command into your custom harness before tool execution.")
	}
	if result.Hooks {
		q.p.info("For Codex, review and trust the hook in /hooks. Configuration does not verify hook execution or Endpoint enrollment.")
	}
}

func (q agentQuestions) Configured(path string) {
	q.p.note("Config: "+TerminalText(path)+"\nMCP sign-in is pending.", "Agent configured")
}

func (q agentQuestions) Ready(result ports.SetupReady) {
	q.p.note(strings.Join([]string{
		"Organization: " + TerminalText(result.Organization),
		"Agent: " + TerminalText(result.Agent),
		"Profile: " + result.ProfileID,
		"Durin MCP: signed in and verified",
		fmt.Sprintf("Available tools: %d", result.ToolCount),
	}, "\n"), "Setup complete")
	if result.ToolCount == 0 {
		q.p.info("No tools are currently available. Ask your administrator to enable a connection or grant access.")
	}
	q.p.outro("Reopen " + TerminalText(result.Agent) + " to load Durin tools and skills. You can also use durin tools or connect an MCP client directly.")
}
