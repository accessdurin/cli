package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
)

func onboardingStatus(step contracts.OnboardingStep, edit ...func(*contracts.OnboardingStatus)) contracts.OnboardingStatus {
	org := "org_one"
	s := contracts.OnboardingStatus{
		OrganizationID: &org, Journey: "organization_creator", Next: "/onboarding", Step: step,
		Profile: &contracts.OnboardingProfile{OrganizationID: org, CreatorID: "user_one", Enrollment: "required"},
	}
	for _, e := range edit {
		e(&s)
	}
	return s
}

func allowed(s *contracts.OnboardingStatus)     { s.Allowed = true }
func tenantReady(s *contracts.OnboardingStatus) { s.TenantReady = true }
func otherOrganization(s *contracts.OnboardingStatus) {
	other := "org_other"
	s.OrganizationID = &other
}

// fakeOnboarding is the server's onboarding state with overridable mutations, recording
// each call.
type fakeOnboarding struct {
	current           contracts.OnboardingStatus
	starts, completes int
	answers           []contracts.OnboardingAnswer
	asked             [][2]any
	payments          []string
	start, complete   func() contracts.OnboardingStatus
	answer            func() contracts.OnboardingStatus
	payment           func()
	clock             *fakeClock
}

func newFakeOnboarding() *fakeOnboarding {
	f := &fakeOnboarding{current: onboardingStatus(contracts.StepTitle), clock: newFakeClock()}
	f.start = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepHeadcount) }
	// The default mutations change the server's state; replacements only return a status.
	f.answer = func() contracts.OnboardingStatus {
		f.current = onboardingStatus(contracts.StepPayment)
		return f.current
	}
	f.complete = func() contracts.OnboardingStatus {
		f.current = onboardingStatus(contracts.StepComplete, allowed)
		return f.current
	}
	f.payment = func() { f.current = onboardingStatus(contracts.StepProvisioning) }
	return f
}

func (f *fakeOnboarding) Status(context.Context) (contracts.OnboardingStatus, error) {
	return f.current, nil
}

func (f *fakeOnboarding) Start(context.Context) error {
	f.starts++
	f.current = f.start()
	return nil
}

func (f *fakeOnboarding) Answer(_ context.Context, a contracts.OnboardingAnswer) (contracts.OnboardingStatus, error) {
	f.answers = append(f.answers, a)
	return f.answer(), nil
}

func (f *fakeOnboarding) Complete(context.Context) (contracts.OnboardingStatus, error) {
	f.completes++
	return f.complete(), nil
}

type fakeOnboardingQuestions struct{ f *fakeOnboarding }

func (q fakeOnboardingQuestions) Answer(_ context.Context, step contracts.OnboardingStep, version int64) (contracts.OnboardingAnswer, error) {
	q.f.asked = append(q.f.asked, [2]any{step, version})
	return contracts.OnboardingAnswer{Step: contracts.StepHeadcount, Version: version, NumberValue: 10}, nil
}

func (q fakeOnboardingQuestions) Payment(_ context.Context, organizationID string) error {
	q.f.payments = append(q.f.payments, organizationID)
	q.f.payment()
	return nil
}

func (f *fakeOnboarding) run() (contracts.OnboardingStatus, error) {
	return OnboardOrganization(context.Background(), OnboardingOptions{Api: f, Questions: fakeOnboardingQuestions{f}, Clock: f.clock, Timeout: 10 * time.Second})
}

func TestOnboardingFollowsServerStepsThroughPaymentAndProvisioning(t *testing.T) {
	f := newFakeOnboarding()
	f.clock.onSleep = func() { f.current = onboardingStatus(contracts.StepComplete, tenantReady) }
	state, err := f.run()
	if err != nil || !state.Allowed {
		t.Fatalf("%+v %v", state, err)
	}
	if !reflect.DeepEqual(f.asked, [][2]any{{contracts.StepHeadcount, int64(0)}}) {
		t.Errorf("asked %v", f.asked)
	}
	if !reflect.DeepEqual(f.answers, []contracts.OnboardingAnswer{{Step: contracts.StepHeadcount, NumberValue: 10}}) {
		t.Errorf("answers %+v", f.answers)
	}
	if !reflect.DeepEqual(f.payments, []string{"org_one"}) || f.completes != 1 {
		t.Errorf("payments %v completes %d", f.payments, f.completes)
	}
}

func TestOnboardingPollsAStalledStepThenExpiresWithoutRepeatingMutations(t *testing.T) {
	for _, question := range []bool{true, false} {
		f := newFakeOnboarding()
		f.start = func() contracts.OnboardingStatus {
			if question {
				return onboardingStatus(contracts.StepHeadcount, tenantReady)
			}
			return onboardingStatus(contracts.StepComplete, tenantReady)
		}
		f.answer = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepHeadcount) }
		f.complete = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepComplete, tenantReady) }
		_, err := f.run()
		wantCliError(t, err, "Organization setup is still pending. Run durin to resume.")
		wantAnswers, wantCompletes := 0, 1
		if question {
			wantAnswers, wantCompletes = 1, 0
		}
		if len(f.answers) != wantAnswers || f.completes != wantCompletes {
			t.Errorf("question=%v: answers %d completes %d", question, len(f.answers), f.completes)
		}
		if elapsed := f.clock.now.Sub(time.Unix(1_700_000_000, 0)); elapsed != 10*time.Second {
			t.Errorf("question=%v: gave up after %v", question, elapsed)
		}
	}
}

func TestOnboardingRejectsAnOrganizationChange(t *testing.T) {
	changed := onboardingStatus(contracts.StepComplete, otherOrganization, allowed)
	sources := map[string]func(f *fakeOnboarding){
		"start":  func(f *fakeOnboarding) { f.start = func() contracts.OnboardingStatus { return changed } },
		"answer": func(f *fakeOnboarding) { f.answer = func() contracts.OnboardingStatus { return changed } },
		"poll":   func(f *fakeOnboarding) { f.payment = func() { f.current = changed } },
		"complete": func(f *fakeOnboarding) {
			f.start = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepComplete, tenantReady) }
			f.complete = func() contracts.OnboardingStatus { return changed }
		},
	}
	for name, change := range sources {
		f := newFakeOnboarding()
		change(f)
		_, err := f.run()
		if err == nil {
			t.Errorf("%s: accepted the change", name)
			continue
		}
		wantCliError(t, err, "Organization changed")
	}
}

func TestOnboardingAcceptsAllowedAccountsAndRefusesInvitedMembers(t *testing.T) {
	f := newFakeOnboarding()
	f.current = onboardingStatus(contracts.StepComplete, func(s *contracts.OnboardingStatus) { s.Journey = "existing" }, allowed)
	if state, err := f.run(); err != nil || !state.Allowed || f.starts != 0 {
		t.Fatalf("%+v %v starts %d", state, err, f.starts)
	}
	f.current = onboardingStatus(contracts.StepProvisioning, func(s *contracts.OnboardingStatus) { s.Journey = "invited_member" })
	_, err := f.run()
	wantCliError(t, err, "Contact your administrator")
	if f.starts != 0 {
		t.Errorf("started onboarding for an invited member")
	}
}

func TestOnboardingReturnsToAQuestionTheServerRequests(t *testing.T) {
	f := newFakeOnboarding()
	f.start = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepProvisioning) }
	f.clock.onSleep = func() { f.current = onboardingStatus(contracts.StepHeadcount) }
	f.answer = func() contracts.OnboardingStatus { return onboardingStatus(contracts.StepComplete, tenantReady) }
	state, err := f.run()
	if err != nil || !state.Allowed {
		t.Fatalf("%+v %v", state, err)
	}
	if len(f.asked) != 1 || f.completes != 1 || len(f.payments) != 0 {
		t.Errorf("asked %v completes %d payments %v", f.asked, f.completes, f.payments)
	}
}

func TestParseOnboardingAnswerTrimsAndValidates(t *testing.T) {
	got, err := ParseOnboardingAnswer(contracts.OnboardingAnswer{Step: contracts.StepSource, Version: 3, TextValue: "other", Other: "  a podcast "})
	if err != nil || got.Other != "a podcast" || got.TextValue != "other" || got.Version != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []contracts.OnboardingAnswer{
		{Step: contracts.StepSource, TextValue: "other"},
		{Step: contracts.StepTitle, TextValue: "   "},
		{Step: contracts.StepHeadcount, NumberValue: 1.5},
		{Step: contracts.StepPayment},
	} {
		if _, err := ParseOnboardingAnswer(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
