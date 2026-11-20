package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// ProfileIDPattern is cliProfileIdSchema's pattern.
var ProfileIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ValidProfileID reports whether cliProfileIdSchema accepts id.
func ValidProfileID(id string) bool { return ProfileIDPattern.MatchString(id) }

// ProfileIDSchema is cliProfileIdSchema.
var ProfileIDSchema = zod.String().Regex(ProfileIDPattern)

// URL is z.url().
func URL() *zod.StringSchema { return zod.String().Format(weburl.ZodURL) }

var personalResource = regexp.MustCompile(`/mcp/u/[a-f0-9]{64}$`)

func scopeHosts(o *jsjson.Object) CliHostScope {
	app, _ := o.GetString("appOrigin")
	issuer, _ := o.GetString("accountIssuer")
	dev, _ := o.GetBool("development")
	return CliHostScope{AppOrigin: app, Development: dev, AccountIssuer: issuer}
}

// CliProfileScopeSchema is cliProfileScopeSchema.
var CliProfileScopeSchema = zod.Object(
	zod.F("appOrigin", URL()),
	// Plain strings, checked as written. z.url() trims, which would accept a padded issuer.
	zod.F("accountIssuer", zod.String().Min(1)),
	zod.F("accountClientId", zod.String().Min(1)),
	zod.F("principalId", zod.String().Min(1)),
	zod.F("workosOrganizationId", zod.String().Min(1)),
	zod.F("organizationId", zod.String().Min(1)),
	zod.F("environment", zod.Enum("sandbox", "production")),
	zod.F("resource", URL()),
	zod.F("mcpIssuer", zod.String().Min(1)),
	zod.F("development", zod.Boolean()),
).Strict().Refine(func(o *jsjson.Object) bool {
	dev, _ := o.GetBool("development")
	for _, key := range []string{"appOrigin", "accountIssuer", "resource", "mcpIssuer"} {
		v, _ := o.GetString(key)
		if !SafeCliURL(v, dev) {
			return false
		}
	}
	return true
}, "Use HTTPS or explicitly enabled loopback development URLs.").Refine(func(o *jsjson.Object) bool {
	app, _ := o.GetString("appOrigin")
	u, err := weburl.Parse(app)
	return err == nil && u.Origin() == app
}, "Use an application origin without a path.").Refine(func(o *jsjson.Object) bool {
	resource, _ := o.GetString("resource")
	return personalResource.MatchString(resource)
}, "Use the resolved personal MCP resource.").SuperRefine(func(o *jsjson.Object, ctx zod.Context) {
	scope := scopeHosts(o)
	for _, role := range CliProfileHostRoles {
		url, _ := o.GetString(string(role))
		if problem := untrustedProfileHost(role, url, scope); problem != "" {
			ctx.AddIssue(problem, string(role))
		}
	}
})

// CliProfileSchema is cliProfileSchema: the scope plus version 1 and id.
var CliProfileSchema = CliProfileScopeSchema.Extend(
	zod.F("version", zod.Literal(jsjson.Number(1))),
	zod.F("id", ProfileIDSchema),
).Strict()

// JSON is the scope as an object in schema order, which JSON.stringify writes for its id.
func (s CliProfileScope) JSON() *jsjson.Object {
	return jsjson.Obj(
		jsjson.KV("appOrigin", jsjson.String(s.AppOrigin)),
		jsjson.KV("accountIssuer", jsjson.String(s.AccountIssuer)),
		jsjson.KV("accountClientId", jsjson.String(s.AccountClientID)),
		jsjson.KV("principalId", jsjson.String(s.PrincipalID)),
		jsjson.KV("workosOrganizationId", jsjson.String(s.WorkosOrganizationID)),
		jsjson.KV("organizationId", jsjson.String(s.OrganizationID)),
		jsjson.KV("environment", jsjson.String(s.Environment)),
		jsjson.KV("resource", jsjson.String(s.Resource)),
		jsjson.KV("mcpIssuer", jsjson.String(s.McpIssuer)),
		jsjson.KV("development", jsjson.Bool(s.Development)),
	)
}

// JSON is {...scope, version: 1, id}.
func (p CliProfile) JSON() *jsjson.Object {
	return p.CliProfileScope.JSON().Set("version", jsjson.Int(int64(p.Version))).Set("id", jsjson.String(p.ID))
}

func scopeFrom(o *jsjson.Object) CliProfileScope {
	get := func(k string) string { v, _ := o.GetString(k); return v }
	dev, _ := o.GetBool("development")
	return CliProfileScope{
		AppOrigin: get("appOrigin"), AccountIssuer: get("accountIssuer"), AccountClientID: get("accountClientId"),
		PrincipalID: get("principalId"), WorkosOrganizationID: get("workosOrganizationId"),
		OrganizationID: get("organizationId"), Environment: get("environment"), Resource: get("resource"),
		McpIssuer: get("mcpIssuer"), Development: dev,
	}
}

// CreateProfile is createProfile: the scope as cliProfileScopeSchema parses it, version 1,
// and id = SHA-256 of JSON.stringify(scope). An invalid scope is a *zod.Error (thrown, so
// unexpected), as is a *TypeError the schema let escape.
func CreateProfile(input CliProfileScope) (profile CliProfile, err error) {
	defer catch(&err)
	out, zerr := zod.SafeParse(CliProfileScopeSchema, input.JSON())
	if zerr != nil {
		return CliProfile{}, zerr
	}
	scope := out.(*jsjson.Object)
	sum := sha256.Sum256([]byte(jsjson.Stringify(scope)))
	return CliProfile{CliProfileScope: scopeFrom(scope), Version: 1, ID: hex.EncodeToString(sum[:])}, nil
}

// SafeParseCliProfile is cliProfileSchema.safeParse(value). issues is non-nil when the value
// is not a valid profile; err is a *TypeError the schema let escape.
func SafeParseCliProfile(value jsjson.Value) (profile CliProfile, issues *zod.Error, err error) {
	defer catch(&err)
	out, zerr := zod.SafeParse(CliProfileSchema, value)
	if zerr != nil {
		return CliProfile{}, zerr, nil
	}
	o := out.(*jsjson.Object)
	id, _ := o.GetString("id")
	return CliProfile{CliProfileScope: scopeFrom(o), Version: 1, ID: id}, nil, nil
}
