package contracts

import (
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// TypeError is a JavaScript TypeError that the TypeScript contracts let escape (new URL on a
// host that only parses inside a non-special URL). The CLI treats it as unexpected.
type TypeError struct{ Message string }

func (e *TypeError) Error() string { return e.Message }

// catch turns a *TypeError panic raised by this package's helpers into an error, as a JS
// caller would see the exception.
func catch(err *error) {
	if r := recover(); r != nil {
		te, ok := r.(*TypeError)
		if !ok {
			panic(r)
		}
		*err = te
	}
}

// mustURL is new URL(value): it throws (panics with *TypeError) when value does not parse.
func mustURL(value string) *weburl.URL {
	u, err := weburl.Parse(value)
	if err != nil {
		panic(&TypeError{Message: "Invalid URL"})
	}
	return u
}

// normalHost is lowercase, without one trailing dot, and with IP addresses written as a URL
// writes them (src/contracts/cli-url.ts).
func normalHost(hostname string) string {
	host := strings.TrimSuffix(strings.ToLower(hostname), ".")
	bare := host
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") && len(bare) >= 2 {
		bare = bare[1 : len(bare)-1]
	}
	candidate := "http://" + bare
	if strings.Contains(bare, ":") {
		candidate = "http://[" + bare + "]"
	}
	if u, err := weburl.Parse(candidate); err == nil {
		return u.Hostname()
	}
	return host
}

// loopbackHosts are localhost and its subdomains, 127.0.0.0/8, ::1, IPv4-mapped 127.x.x.x,
// and the unspecified addresses 0.0.0.0 and ::, which reach this computer too.
var loopbackHosts = []*regexp.Regexp{
	regexp.MustCompile(`^(?:.+\.)?localhost$`),
	regexp.MustCompile(`^127\.\d+\.\d+\.\d+$`),
	regexp.MustCompile(`^0\.0\.0\.0$`),
	regexp.MustCompile(`^\[::1?\]$`),
	regexp.MustCompile(`^\[::ffff:(?:7f[\da-f]{2}:[\da-f]{1,4}|0:0)\]$`),
}

// LoopbackCliHost reports whether hostname reaches this computer.
func LoopbackCliHost(hostname string) bool {
	host := normalHost(hostname)
	for _, p := range loopbackHosts {
		if p.MatchString(host) {
			return true
		}
	}
	return false
}

// SafeCliURL reports whether value is an HTTPS URL (or, with development, a loopback HTTP
// URL) without credentials, query, fragment or line breaks.
func SafeCliURL(value string, development bool) bool {
	u, err := weburl.Parse(value)
	if err != nil {
		return false
	}
	local := development && u.Protocol() == "http:" && LoopbackCliHost(u.Hostname())
	return (u.Protocol() == "https:" || local) && u.Username() == "" && u.Password() == "" &&
		u.Hash() == "" && u.Search() == "" && !strings.ContainsAny(value, "\r\n")
}

// opaqueIssuer: spaces (ECMAScript whitespace and line terminators), NUL and backslashes are
// refused outright, as is anything that does not parse.
func opaqueIssuer(value string) bool {
	for _, r := range value {
		if r == 0 || r == '\\' || zod.IsJSWhiteSpace(r) {
			return true
		}
	}
	return !weburl.CanParse(value)
}

// extraSlash: no "://", or a "//" anywhere after it.
func extraSlash(value string) bool {
	scheme := strings.Index(value, "://")
	return scheme < 0 || strings.Contains(value[scheme+3:], "//")
}

// RoundTripCliURL reports whether value is its own URL form (src/contracts/cli-url.ts). A
// self-hosted issuer is stored as written, so spaces, NUL and backslashes are refused, and so is
// any spelling whose URL form is not the string itself (HTTPS://, an explicit :443, or //).
// https://<host> and https://<host>/ are the same issuer: the URL form adds the slash.
func RoundTripCliURL(value string) bool {
	if opaqueIssuer(value) {
		return false
	}
	u, err := weburl.Parse(value)
	if err != nil {
		return false
	}
	href := u.Href()
	return !extraSlash(value) && (href == value || (u.Pathname() == "/" && href == value+"/"))
}
