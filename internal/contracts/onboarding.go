package contracts

import (
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// DiscoverySource is one answer to "where did you hear about Durin?".
type DiscoverySource struct{ Value, Label string }

// DiscoverySources is DISCOVERY_SOURCES, in display order.
var DiscoverySources = []DiscoverySource{
	{"search", "Search"},
	{"recommendation", "Recommendation"},
	{"linkedin", "LinkedIn"},
	{"x", "X"},
	{"community", "Community"},
	{"article", "Article"},
	{"other", "Other"},
}

// Onboarding schemas (src/contracts/onboarding.ts).
var (
	DiscoverySourceSchema = zod.Enum("search", "recommendation", "linkedin", "x", "community", "article", "other")
	OnboardingStepSchema  = zod.Enum("title", "headcount", "source", "payment", "provisioning", "complete")

	OnboardingProfileSchema = zod.Object(
		zod.F("organizationId", zod.String()),
		zod.F("creatorId", zod.String()),
		zod.F("enrollment", zod.Enum("required", "exempt")),
		zod.F("title", zod.Nullable(zod.String().Trim().Min(1).Max(120))),
		zod.F("headcount", zod.Nullable(zod.Number().Int().Min(1).Max(100_000_000))),
		zod.F("source", zod.Nullable(DiscoverySourceSchema)),
		zod.F("otherSource", zod.Nullable(zod.String().Trim().Min(1).Max(240))),
		zod.F("paymentReady", zod.Boolean()),
		zod.F("completedAt", zod.Nullable(zod.Number().Int())),
		zod.F("version", zod.Number().Int().Nonnegative()),
	)

	answerVersion = zod.F("version", zod.Number().Int().Nonnegative())

	OnboardingAnswerSchema = zod.DiscriminatedUnion("step",
		zod.Object(answerVersion, zod.F("step", zod.Literal(jsjson.String("title"))), zod.F("value", zod.String().Trim().Min(1).Max(120))).Strict(),
		zod.Object(answerVersion, zod.F("step", zod.Literal(jsjson.String("headcount"))), zod.F("value", zod.Number().Int().Min(1).Max(100_000_000))).Strict(),
		zod.Object(
			answerVersion,
			zod.F("step", zod.Literal(jsjson.String("source"))),
			zod.F("value", DiscoverySourceSchema),
			zod.F("other", zod.Optional(zod.String().Trim().Min(1).Max(240))),
		).Strict().Refine(func(o *jsjson.Object) bool {
			value, _ := o.GetString("value")
			return value != "other" || truthy(o, "other")
		}, "Tell us where you heard about Durin.", "other"),
	)

	OnboardingAccessSchema = zod.Object(
		zod.F("organizationId", zod.Nullable(zod.String())),
		zod.F("journey", zod.Enum("organization_creator", "invited_member", "existing", "unassigned")),
		zod.F("next", zod.Enum("/onboarding", "/connections")),
		zod.F("allowed", zod.Boolean()),
	)

	OnboardingStatusSchema = OnboardingAccessSchema.Extend(
		zod.F("profile", zod.Nullable(OnboardingProfileSchema)),
		zod.F("step", OnboardingStepSchema),
		zod.F("tenantReady", zod.Boolean()),
	)
)

// ParseOnboardingStatus is onboardingStatusSchema.parse into OnboardingStatus.
func ParseOnboardingStatus(v jsjson.Value) (OnboardingStatus, error) {
	out, err := zod.Parse(OnboardingStatusSchema, v)
	if err != nil {
		return OnboardingStatus{}, err
	}
	var status OnboardingStatus
	if err := jsjson.Decode(out, &status); err != nil {
		return OnboardingStatus{}, err
	}
	return status, nil
}

// JSON is the answer as onboardingAnswerSchema writes it, in schema order.
func (a OnboardingAnswer) JSON() *jsjson.Object {
	o := jsjson.Obj(jsjson.KV("version", jsjson.Int(a.Version)), jsjson.KV("step", jsjson.String(string(a.Step))))
	switch a.Step {
	case StepHeadcount:
		o.Set("value", jsjson.Number(a.NumberValue))
	default:
		o.Set("value", jsjson.String(a.TextValue))
		if a.Step == StepSource && a.Other != "" {
			o.Set("other", jsjson.String(a.Other))
		}
	}
	return o
}
