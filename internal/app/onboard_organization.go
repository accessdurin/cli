package app

import (
	"context"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// OnboardingOptions are what onboarding an organization needs. Clock defaults to the real
// clock and Timeout to ten minutes.
type OnboardingOptions struct {
	Api       ports.OrganizationOnboardingApi
	Questions ports.OrganizationQuestions
	Clock     ports.PollingClock
	Timeout   time.Duration
}

const (
	onboardingTimeout  = 10 * time.Minute
	onboardingInterval = 2 * time.Second
)

type onboardingFlow struct {
	OnboardingOptions
	deadline       time.Time
	organizationID *string
}

func (f *onboardingFlow) assertFresh() error {
	if !f.Clock.Now().Before(f.deadline) {
		return clierror.New("Organization setup is still pending. Run durin to resume.")
	}
	return nil
}

func (f *onboardingFlow) checked(state contracts.OnboardingStatus) (contracts.OnboardingStatus, error) {
	if !sameOrganization(state.OrganizationID, f.organizationID) {
		return state, clierror.New("Organization changed during onboarding. Run durin to sign in again.")
	}
	if !state.Allowed && state.Journey != "organization_creator" {
		return state, clierror.New("Your organization is not ready. Contact your administrator, then run durin again.")
	}
	return state, nil
}

// ParseOnboardingAnswer is onboardingAnswerSchema.parse: the answer validated, with its text
// trimmed as the schema trims it.
func ParseOnboardingAnswer(answer contracts.OnboardingAnswer) (contracts.OnboardingAnswer, error) {
	parsed, err := zod.Parse(contracts.OnboardingAnswerSchema, answer.JSON())
	if err != nil {
		return contracts.OnboardingAnswer{}, err
	}
	o := parsed.(*jsjson.Object)
	step, _ := o.GetString("step")
	version, _ := o.GetNumber("version")
	out := contracts.OnboardingAnswer{Step: contracts.OnboardingStep(step), Version: int64(version)}
	if out.Step == contracts.StepHeadcount {
		out.NumberValue, _ = o.GetNumber("value")
	} else {
		out.TextValue, _ = o.GetString("value")
		out.Other, _ = o.GetString("other")
	}
	return out, nil
}

type stepHandler func(ctx context.Context, f *onboardingFlow, state contracts.OnboardingStatus) (contracts.OnboardingStatus, error)

func question(step contracts.OnboardingStep) stepHandler {
	return func(ctx context.Context, f *onboardingFlow, state contracts.OnboardingStatus) (contracts.OnboardingStatus, error) {
		if state.Profile == nil {
			return state, clierror.New("Organization onboarding is unavailable for this account.")
		}
		given, err := f.Questions.Answer(ctx, step, state.Profile.Version)
		if err != nil {
			return state, err
		}
		answer, err := ParseOnboardingAnswer(given)
		if err != nil {
			return state, err
		}
		if answer.Step != step {
			return state, clierror.New("Answer did not match the current onboarding question.")
		}
		if err := f.assertFresh(); err != nil {
			return state, err
		}
		return f.Api.Answer(ctx, answer)
	}
}

// waitUntil polls every two seconds, until the deadline, until ready accepts the status.
func (f *onboardingFlow) waitUntil(ctx context.Context, initial contracts.OnboardingStatus, ready func(contracts.OnboardingStatus) bool) (contracts.OnboardingStatus, error) {
	state, err := f.checked(initial)
	for err == nil && !ready(state) {
		if err = f.assertFresh(); err != nil {
			break
		}
		if err = f.Clock.Sleep(ctx, min(onboardingInterval, f.deadline.Sub(f.Clock.Now()))); err != nil {
			break
		}
		if err = f.assertFresh(); err != nil {
			break
		}
		if state, err = f.Api.Status(ctx); err == nil {
			state, err = f.checked(state)
		}
	}
	return state, err
}

func completeStep(ctx context.Context, f *onboardingFlow, state contracts.OnboardingStatus) (contracts.OnboardingStatus, error) {
	ready, err := f.waitUntil(ctx, state, func(value contracts.OnboardingStatus) bool {
		return value.Allowed || value.TenantReady || value.Step != state.Step
	})
	if err != nil || ready.Allowed || ready.Step != state.Step {
		return ready, err
	}
	if err := f.assertFresh(); err != nil {
		return ready, err
	}
	return f.Api.Complete(ctx)
}

func paymentStep(ctx context.Context, f *onboardingFlow, state contracts.OnboardingStatus) (contracts.OnboardingStatus, error) {
	if state.OrganizationID == nil || *state.OrganizationID == "" {
		return state, clierror.New("Select an organization before payment setup.")
	}
	if err := f.Questions.Payment(ctx, *state.OrganizationID); err != nil {
		return state, err
	}
	return f.waitUntil(ctx, state, func(value contracts.OnboardingStatus) bool {
		return value.Allowed || value.Step != contracts.StepPayment
	})
}

var onboardingSteps = map[contracts.OnboardingStep]stepHandler{
	contracts.StepTitle:        question(contracts.StepTitle),
	contracts.StepHeadcount:    question(contracts.StepHeadcount),
	contracts.StepSource:       question(contracts.StepSource),
	contracts.StepPayment:      paymentStep,
	contracts.StepProvisioning: completeStep,
	contracts.StepComplete:     completeStep,
}

func (f *onboardingFlow) advance(ctx context.Context) (contracts.OnboardingStatus, error) {
	state, err := f.Api.Status(ctx)
	if err != nil {
		return state, err
	}
	if state, err = f.checked(state); err != nil {
		return state, err
	}
	for !state.Allowed {
		if err := f.assertFresh(); err != nil {
			return state, err
		}
		step := state.Step
		next, err := onboardingSteps[step](ctx, f, state)
		if err != nil {
			return next, err
		}
		// A mutation can return before its effects are visible. Poll instead of repeating it.
		if state, err = f.waitUntil(ctx, next, func(value contracts.OnboardingStatus) bool {
			return value.Allowed || value.Step != step
		}); err != nil {
			return state, err
		}
	}
	return state, nil
}

// OnboardOrganization walks the organization creator through the server's onboarding steps
// until the organization is allowed, polling (never repeating a mutation) while the server
// catches up, for at most Timeout (src/onboard-organization.ts).
func OnboardOrganization(ctx context.Context, options OnboardingOptions) (contracts.OnboardingStatus, error) {
	state, err := options.Api.Status(ctx)
	if err != nil || state.Allowed {
		return state, err
	}
	if options.Clock == nil {
		options.Clock = NewPollingClock()
	}
	if options.Timeout == 0 {
		options.Timeout = onboardingTimeout
	}
	flow := &onboardingFlow{OnboardingOptions: options, deadline: options.Clock.Now().Add(options.Timeout), organizationID: state.OrganizationID}
	if _, err := flow.checked(state); err != nil {
		return state, err
	}
	if err := options.Api.Start(ctx); err != nil {
		return state, err
	}
	return flow.advance(ctx)
}
