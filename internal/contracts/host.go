package contracts

import (
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/weburl"
)

// CliHostRole is what a host is trusted for (src/contracts/cli-host.ts).
type CliHostRole string

const (
	RoleResource        CliHostRole = "resource"
	RoleMcpIssuer       CliHostRole = "mcpIssuer"
	RoleAccountIssuer   CliHostRole = "accountIssuer"
	RoleAccountProvider CliHostRole = "accountProvider"
)

// CliHostRoles lists every role.
var CliHostRoles = []CliHostRole{RoleResource, RoleMcpIssuer, RoleAccountIssuer, RoleAccountProvider}

// CliProfileHostRoles are the roles a saved profile records a URL for.
var CliProfileHostRoles = []CliHostRole{RoleResource, RoleMcpIssuer, RoleAccountIssuer}

// contactedRoles are the roles setup sends requests to. The account issuer only names who
// signs account tokens.
var contactedRoles = []CliHostRole{RoleResource, RoleMcpIssuer, RoleAccountProvider}

// CliHostScope is the deployment a URL is judged against. AccountIssuer is empty until known:
// once known, a WorkOS account issuer lets a self-hosted app use WorkOS's account API.
type CliHostScope struct {
	AppOrigin     string
	Development   bool
	AccountIssuer string
}

// workosAPI is the account API for device sign-in. durin's server returns exactly this as
// providerOrigin outside loopback development (durin workers/api/src/cli-auth.ts).
const workosAPI = "api.workos.com"

// workosIssuerHost is a WorkOS account issuer's host: an AuthKit tenant, which is one DNS
// label under authkit.app (deeper names aren't tenants), or WorkOS's own API. Only over
// HTTPS on the default port.
var workosIssuerHost = regexp.MustCompile(`^(?:[a-z\d](?:[a-z\d-]{0,61}[a-z\d])?\.authkit\.app|api\.workos\.com)$`)

func workosIssuer(url string) bool {
	parsed, err := weburl.Parse(url)
	return err == nil && parsed.Protocol() == "https:" && workosIssuerHost.MatchString(parsed.Host())
}

// deploymentHosts are the exact hosts each known app uses. Never match by suffix. Its issuers
// must be exactly https://<host>, and the app's own host serves only appHostRoles.
var deploymentHosts = map[string]map[CliHostRole][]string{
	// Account issuer: the "Durin Production" WorkOS environment's verified AuthKit custom
	// domain, which https://auth.getdurin.com/.well-known/openid-configuration names as its
	// issuer.
	"app.getdurin.com": {
		RoleResource:        {"mcp.getdurin.com"},
		RoleMcpIssuer:       {"auth.getdurin.com"},
		RoleAccountIssuer:   {"auth.getdurin.com"},
		RoleAccountProvider: {workosAPI},
	},
	// Gateway: durin infra/durin.config.json (staging mcpGateway).
	// Issuers: the staging AuthKit tenant, which signs both staging MCP grants and account tokens.
	"moria.app.getdurin.com": {
		RoleResource:        {"moria.mcp.getdurin.com"},
		RoleMcpIssuer:       {"resilient-tiger-64-staging.authkit.app"},
		RoleAccountIssuer:   {"resilient-tiger-64-staging.authkit.app"},
		RoleAccountProvider: {workosAPI},
	},
}

var roleNames = map[CliHostRole]string{
	RoleResource:        "MCP endpoint",
	RoleMcpIssuer:       "MCP sign-in server",
	RoleAccountIssuer:   "account sign-in issuer",
	RoleAccountProvider: "account sign-in service",
}

// hostOf is the hostname and port, which a URL leaves out when it is the default for its
// scheme; "" when value does not parse.
func hostOf(value string) string {
	u, err := weburl.Parse(value)
	if err != nil {
		return ""
	}
	return u.Host()
}

func loopback(host string) bool { return LoopbackCliHost(mustURL("http://" + host).Hostname()) }

// dottedApp: cliOptions strips --app-url's one trailing dot, so an app host still ending in
// one names no deployment: durin trusts no host for it, and never treats it as self-hosted.
func dottedApp(scope CliHostScope) bool {
	u, err := weburl.Parse(scope.AppOrigin)
	return err == nil && strings.HasSuffix(u.Hostname(), ".")
}

func selfHosted(scope CliHostScope) bool {
	_, known := deploymentHosts[hostOf(scope.AppOrigin)]
	return !known
}

// selfHostedHosts: a self-hosted app may sign accounts in with WorkOS; only then may it reach
// WorkOS's API.
func selfHostedHosts(role CliHostRole, scope CliHostScope) []string {
	if role == RoleAccountProvider && workosIssuer(scope.AccountIssuer) {
		return []string{workosAPI}
	}
	return nil
}

// A known app's own host is never one of its issuers. A self-hosted app's serves every role.
func appHosts(role CliHostRole, scope CliHostScope) []string {
	if selfHosted(scope) || role == RoleResource || role == RoleAccountProvider {
		return []string{hostOf(scope.AppOrigin)}
	}
	return nil
}

func expectedHosts(role CliHostRole, scope CliHostScope) []string {
	if dottedApp(scope) {
		return nil
	}
	listed := selfHostedHosts(role, scope)
	if hosts, ok := deploymentHosts[hostOf(scope.AppOrigin)]; ok {
		listed = hosts[role]
	}
	var out []string
	for _, host := range unique(append(append([]string(nil), listed...), appHosts(role, scope)...)) {
		if host != "" && (scope.Development || !loopback(host)) {
			out = append(out, host)
		}
	}
	return out
}

func unique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func allowsWorkosIssuer(role CliHostRole, scope CliHostScope) bool {
	return role == RoleAccountIssuer && selfHosted(scope)
}

func pinnedIssuer(role CliHostRole, scope CliHostScope) bool {
	return (role == RoleMcpIssuer || role == RoleAccountIssuer) && !selfHosted(scope)
}

// exactIssuer is exactly https://<host> or https://<host>/, compared as written: no
// uppercase, port, path, query, fragment, user, or other spelling that only parses to the
// same address.
func exactIssuer(url, host string) bool {
	return url == "https://"+host || url == "https://"+host+"/"
}

func allowedRemote(role CliHostRole, url string, scope CliHostScope) bool {
	if pinnedIssuer(role, scope) {
		for _, expected := range expectedHosts(role, scope) {
			if exactIssuer(url, expected) {
				return true
			}
		}
		return false
	}
	return contains(expectedHosts(role, scope), hostOf(url)) ||
		(allowsWorkosIssuer(role, scope) && workosIssuer(url))
}

func allowedHost(role CliHostRole, url string, scope CliHostScope) bool {
	if loopback(hostOf(url)) {
		return scope.Development
	}
	return allowedRemote(role, url, scope)
}

func describeExpected(role CliHostRole, scope CliHostScope) string {
	expected := expectedHosts(role, scope)
	if allowsWorkosIssuer(role, scope) {
		expected = append(expected, "a WorkOS issuer (<tenant>.authkit.app or api.workos.com)")
	}
	if len(expected) == 0 {
		return ""
	}
	return ", but the durin app at " + hostOf(scope.AppOrigin) + " uses " + strings.Join(expected, " or ")
}

func workosProviderHint(role CliHostRole, host string, scope CliHostScope) string {
	if role == RoleAccountProvider && host == workosAPI && selfHosted(scope) {
		return " A self-hosted durin app uses api.workos.com only when its account sign-in issuer is a WorkOS issuer."
	}
	return ""
}

func describeProblem(role CliHostRole, host string, scope CliHostScope) string {
	if pinnedIssuer(role, scope) && contains(expectedHosts(role, scope), host) {
		return "The " + roleNames[role] + " must be exactly " + host + " over HTTPS, in lowercase, with no port, path, query or user name."
	}
	local := ""
	if loopback(host) {
		local = " Local addresses are only allowed with --dev."
	}
	return "The " + roleNames[role] + " is on " + host + describeExpected(role, scope) + "." + local + workosProviderHint(role, host, scope)
}

func untrustedCliHost(role CliHostRole, url string, scope CliHostScope) string {
	host := hostOf(url)
	if host == "" {
		return "The " + roleNames[role] + " is not a valid web address."
	}
	if allowedHost(role, url, scope) {
		return ""
	}
	return describeProblem(role, host, scope)
}

// UntrustedCliHost explains, in plain language, why url is not on a host that the deployment
// at scope.AppOrigin uses for role; "" when the host is expected. err is a *TypeError where
// the TypeScript function threw.
func UntrustedCliHost(role CliHostRole, url string, scope CliHostScope) (problem string, err error) {
	defer catch(&err)
	return untrustedCliHost(role, url, scope), nil
}

// CanonicalIssuer is the issuer to save and compare for role. A known app's pinned issuer is
// https://<host> with no trailing slash, as its sign-in server names itself in RFC 8414
// metadata and the callback's iss. Any other issuer, including a self-hosted app's, is
// returned as written.
func CanonicalIssuer(role CliHostRole, url string, scope CliHostScope) (issuer string, err error) {
	defer catch(&err)
	if !pinnedIssuer(role, scope) {
		return url, nil
	}
	for _, expected := range expectedHosts(role, scope) {
		if exactIssuer(url, expected) {
			return "https://" + expected, nil
		}
	}
	return url, nil
}

// CliHostAllowlist is the hosts a request may reach. Loopback hosts are reachable only in
// development.
type CliHostAllowlist struct {
	Hosts       []string
	Development bool
}

// DeploymentHostAllowlist is every host setup may contact for the deployment at
// scope.AppOrigin. A self-hosted app's list has api.workos.com only once scope.AccountIssuer
// is known to be a WorkOS issuer.
func DeploymentHostAllowlist(scope CliHostScope) (allowlist CliHostAllowlist, err error) {
	defer catch(&err)
	var hosts []string
	for _, role := range contactedRoles {
		hosts = append(hosts, expectedHosts(role, scope)...)
	}
	return CliHostAllowlist{Hosts: unique(hosts), Development: scope.Development}, nil
}

// ProfileHostAllowlist: a saved profile's MCP requests reach only its endpoint and sign-in
// server.
func ProfileHostAllowlist(resource, mcpIssuer string, development bool) CliHostAllowlist {
	return CliHostAllowlist{Hosts: unique([]string{hostOf(resource), hostOf(mcpIssuer)}), Development: development}
}

func listedHost(host string, allowlist CliHostAllowlist) bool {
	if loopback(host) {
		return allowlist.Development
	}
	return contains(allowlist.Hosts, host)
}

func unlistedReason(host string, allowlist CliHostAllowlist) string {
	if loopback(host) {
		return "Local addresses are only allowed with --dev."
	}
	return "It isn’t one of the hosts this durin deployment uses (" + strings.Join(allowlist.Hosts, ", ") + ")."
}

// UnlistedCliHost explains, in plain language, why url is not on allowlist; "" when it is.
func UnlistedCliHost(url string, allowlist CliHostAllowlist) (problem string, err error) {
	defer catch(&err)
	host := hostOf(url)
	if host == "" {
		return "durin didn’t send the request: its address is not a valid web address.", nil
	}
	if listedHost(host, allowlist) {
		return "", nil
	}
	return "durin didn’t contact " + host + ". " + unlistedReason(host, allowlist) + " Nothing was sent to it.", nil
}
