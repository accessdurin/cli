package contracts

import (
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// Primitives (src/contracts/primitives.ts).
var (
	RoleSchema        = zod.Enum("admin", "security", "member", "auditor")
	EnvironmentSchema = zod.Enum("sandbox", "production")
	CountSchema       = zod.Number().Int().Nonnegative()
)

// --- cli-auth.ts ---------------------------------------------------------------------------

// CliAuthConfigSchema is cliAuthConfigSchema (GET /auth/cli/config).
var CliAuthConfigSchema = zod.Object(
	zod.F("clientId", zod.String().Min(1)),
	zod.F("issuer", URL()),
	zod.F("providerOrigin", URL()),
	zod.F("appOrigin", URL()),
).Strict()

// DeviceChallengeSchema is deviceChallengeSchema.
var DeviceChallengeSchema = zod.Object(
	zod.F("device_code", zod.String().Min(1)),
	zod.F("user_code", zod.String().Min(1)),
	zod.F("verification_uri", URL()),
	zod.F("verification_uri_complete", zod.Optional(URL())),
	zod.F("expires_in", zod.Number().Int().Positive().Max(1800)),
	zod.F("interval", zod.Default(zod.Number().Int().Positive().Max(60), func() jsjson.Value { return jsjson.Number(5) })),
)

// DeviceSessionSchema is deviceSessionSchema: its output is {accessToken, refreshToken,
// organizationId (string or null), principalId}.
var DeviceSessionSchema = zod.Transform(zod.Object(
	zod.F("access_token", zod.String().Min(1)),
	zod.F("refresh_token", zod.String().Min(1)),
	zod.F("organization_id", zod.Nullish(zod.String().Min(1))),
	zod.F("user", zod.Object(zod.F("id", zod.String().Min(1)))),
), func(v jsjson.Value) jsjson.Value {
	o := v.(*jsjson.Object)
	access, _ := o.Get("access_token")
	refresh, _ := o.Get("refresh_token")
	org, _ := o.Get("organization_id")
	if org == nil {
		org = jsjson.Null{}
	}
	user, _ := o.GetObject("user")
	principal, _ := user.Get("id")
	return jsjson.Obj(
		jsjson.KV("accessToken", access),
		jsjson.KV("refreshToken", refresh),
		jsjson.KV("organizationId", org),
		jsjson.KV("principalId", principal),
	)
})

// ParseDeviceSession parses a device token response into an AccountSession.
func ParseDeviceSession(v jsjson.Value) (AccountSession, error) {
	out, err := zod.Parse(DeviceSessionSchema, v)
	if err != nil {
		return AccountSession{}, err
	}
	o := out.(*jsjson.Object)
	s := AccountSession{}
	s.AccessToken, _ = o.GetString("accessToken")
	s.RefreshToken, _ = o.GetString("refreshToken")
	s.PrincipalID, _ = o.GetString("principalId")
	if org, ok := o.GetString("organizationId"); ok {
		s.OrganizationID = &org
	}
	return s, nil
}

// DeviceErrorSchema is deviceErrorSchema.
var DeviceErrorSchema = zod.Object(zod.F("error", zod.String()))

// AuthorizationCallbackSchema is authorizationCallbackSchema: its output is {kind: "code",
// code, state, issuer} or {kind: "denied", state, issuer}, issuer undefined when absent.
var AuthorizationCallbackSchema = zod.Transform(zod.Object(
	zod.F("state", zod.String().Min(1).Max(256)),
	zod.F("code", zod.Optional(zod.String().Min(1).Max(4096))),
	zod.F("error", zod.Optional(zod.String().Min(1).Max(256))),
	zod.F("iss", zod.Optional(URL())),
).Refine(func(o *jsjson.Object) bool {
	return truthy(o, "code") != truthy(o, "error")
}, "Invalid input"), func(v jsjson.Value) jsjson.Value {
	o := v.(*jsjson.Object)
	state, _ := o.Get("state")
	iss, _ := o.Get("iss")
	if truthy(o, "code") {
		code, _ := o.Get("code")
		return jsjson.Obj(jsjson.KV("kind", jsjson.String("code")), jsjson.KV("code", code), jsjson.KV("state", state), jsjson.KV("issuer", iss))
	}
	return jsjson.Obj(jsjson.KV("kind", jsjson.String("denied")), jsjson.KV("state", state), jsjson.KV("issuer", iss))
})

func truthy(o *jsjson.Object, key string) bool {
	s, ok := o.GetString(key)
	return ok && s != ""
}

// --- account.ts ----------------------------------------------------------------------------

// SessionSchema is sessionSchema (the durin session).
var SessionSchema = zod.Object(
	zod.F("mode", zod.Enum("dev", "workos")),
	zod.F("principalId", zod.String()),
	zod.F("name", zod.String()),
	zod.F("email", zod.String()),
	zod.F("emailVerified", zod.Optional(zod.Boolean())),
	zod.F("role", RoleSchema),
	zod.F("organizationId", zod.String()),
	zod.F("organizations", zod.Array(zod.Object(zod.F("id", zod.String()), zod.F("name", zod.String())))),
	zod.F("onboarding", zod.Optional(OnboardingAccessSchema)),
)

// ApiErrorSchema is apiErrorSchema.
var ApiErrorSchema = zod.Object(
	zod.F("error", zod.Object(zod.F("code", zod.String()), zod.F("message", zod.String()))),
	zod.F("requestId", zod.Optional(zod.String())),
)

// OrganizationCreatedSchema is organizationCreatedSchema.
var OrganizationCreatedSchema = zod.Object(
	zod.F("organizationId", zod.String()),
	zod.F("workosOrganizationId", zod.String()),
	zod.F("status", zod.Enum("provisioning_pending", "ready")),
)

// OrganizationCreateSchema is organizationCreateSchema.
var OrganizationCreateSchema = zod.Object(
	zod.F("name", zod.String().Min(2).Max(80)),
	zod.F("idempotencyKey", zod.UUID()),
).Strict()

// --- personal-bootstrap.ts -----------------------------------------------------------------

// OrganizationEntitlementsSchema is organizationEntitlementsSchema.
var OrganizationEntitlementsSchema = zod.Object(
	zod.F("organizationId", zod.String().Min(1)),
	zod.F("package", zod.Enum("standard", "enterprise")),
).Strict()

// PersonalWorkspaceSchema is personalWorkspaceSchema.
var PersonalWorkspaceSchema = zod.Object(
	zod.F("organization", zod.Object(zod.F("id", zod.String()), zod.F("name", zod.String()), zod.F("environment", EnvironmentSchema))),
	zod.F("membership", zod.Object(
		zod.F("principalId", zod.String()),
		zod.F("role", RoleSchema),
		zod.F("active", zod.Boolean()),
		zod.F("revocationEpoch", CountSchema),
	)),
).Strict()

// PersonalBootstrapSchema is personalBootstrapSchema (still strict, as extend keeps it).
var PersonalBootstrapSchema = PersonalWorkspaceSchema.Extend(
	zod.F("entitlements", OrganizationEntitlementsSchema),
	zod.F("session", SessionSchema),
	zod.F("mcp", McpEndpointSchema),
	zod.F("settings", zod.Object(
		zod.F("retention", zod.Literal(jsjson.String("metadata_only"))),
		zod.F("environment", EnvironmentSchema),
	)),
)
