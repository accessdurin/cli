// Package weburl gives Go code the semantics of the WHATWG URL class exactly as Node 22
// implements it (with ada 2.9), because the CLI's security checks were written against them:
// host allowlists, HTTPS-only rules, loopback detection, and exact string comparisons such as
// value == Parse(value).Href() and Parse(origin).Origin() == origin.
//
// Every behavior is pinned by differential fixtures generated from Node itself
// (testdata/gen.mjs), including the places where Node (ada 2.9.2) differs from the current URL
// Standard, which this package reproduces on purpose:
//
//   - '^' is not percent-encoded in paths;
//   - a final ".." on a non-special URL whose path is already empty adds no empty segment
//     (new URL("mcp://x/..") is "mcp://x", not "mcp://x/");
//   - a relative reference containing '#' resolves against an opaque base
//     (new URL("/x#f", "foo:opaque") is "foo:/x#f", and "x#f" gives "foo:opaque/x#f");
//   - the host setters follow ada's set_host_or_hostname (for example, an empty hostname
//     leaves "sc:/p" alone, and host = "h:a1" on "http://x:81/" clears the port);
//   - IDNA is ada's own UTS #46 implementation, ported with its tables (see idna.go).
//
// net/url must not be used for these checks: it parses differently (no IDNA, no IPv4 number
// forms, no backslash or tab handling, different percent-encoding).
//
// Strings are sequences of Unicode code points here, as JavaScript's USVStrings are: invalid
// UTF-8 in an input is read as U+FFFD, just as Node replaces a lone surrogate.
package weburl

import (
	"errors"
	"strconv"
	"strings"
)

// ErrInvalidURL is returned for any input Node's URL constructor rejects with
// ERR_INVALID_URL ("Invalid URL").
var ErrInvalidURL = errors.New("Invalid URL")

// URL is a parsed URL record. The zero value is not a valid URL; use Parse or ParseRef.
type URL struct {
	scheme      string
	username    string
	password    string
	host        host
	hasHost     bool
	port        int // -1 when null
	opaque      bool
	opaquePath  string
	path        []string
	query       string
	hasQuery    bool
	fragment    string
	hasFragment bool
}

// Parse is new URL(input).
func Parse(input string) (*URL, error) {
	u, ok := basicParse(input, nil, nil, noOverride)
	if !ok {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// ParseRef is new URL(input, base): input resolved against base. It fails when base itself
// does not parse, as Node's constructor does.
func ParseRef(input, base string) (*URL, error) {
	b, ok := basicParse(base, nil, nil, noOverride)
	if !ok {
		return nil, ErrInvalidURL
	}
	u, ok := basicParse(input, b, nil, noOverride)
	if !ok {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// CanParse is URL.canParse(input), which is whether Parse succeeds. It follows the constructor
// even where Node 22's URL.canParse does not: once V8 optimizes canParse (after a few thousand
// calls), Node rejects one-byte strings holding U+0080..U+00FF, such as "http://ß/", that
// new URL() accepts.
func CanParse(input string) bool {
	_, ok := basicParse(input, nil, nil, noOverride)
	return ok
}

// CanParseRef is URL.canParse(input, base), which is whether ParseRef succeeds.
func CanParseRef(input, base string) bool {
	_, err := ParseRef(input, base)
	return err == nil
}

// Clone returns an independent copy, like new URL(url).
func (u *URL) Clone() *URL {
	c := *u
	c.path = append([]string(nil), u.path...)
	return &c
}

func (u *URL) special() bool { return isSpecialScheme(u.scheme) }

func (u *URL) includesCredentials() bool { return u.username != "" || u.password != "" }

func (u *URL) pathString() string {
	if u.opaque {
		return u.opaquePath
	}
	var b strings.Builder
	for _, seg := range u.path {
		b.WriteByte('/')
		b.WriteString(seg)
	}
	return b.String()
}

// Href is url.href (and String(url), url.toString(), url.toJSON()).
func (u *URL) Href() string {
	var b strings.Builder
	b.WriteString(u.scheme)
	b.WriteByte(':')
	if u.hasHost {
		b.WriteString("//")
		if u.includesCredentials() {
			b.WriteString(u.username)
			if u.password != "" {
				b.WriteByte(':')
				b.WriteString(u.password)
			}
			b.WriteByte('@')
		}
		b.WriteString(u.host.String())
		if u.port != -1 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(u.port))
		}
	} else if !u.opaque && len(u.path) > 1 && u.path[0] == "" {
		b.WriteString("/.")
	}
	b.WriteString(u.pathString())
	if u.hasQuery {
		b.WriteByte('?')
		b.WriteString(u.query)
	}
	if u.hasFragment {
		b.WriteByte('#')
		b.WriteString(u.fragment)
	}
	return b.String()
}

// String is Href.
func (u *URL) String() string { return u.Href() }

// Protocol is url.protocol: the scheme followed by ':'.
func (u *URL) Protocol() string { return u.scheme + ":" }

// Username is url.username, percent-encoded as it appears in Href.
func (u *URL) Username() string { return u.username }

// Password is url.password, percent-encoded as it appears in Href.
func (u *URL) Password() string { return u.password }

// Host is url.host: Hostname, then ':' and the port unless the port is null (absent or the
// scheme's default). Like Node, it is "" when the hostname is empty, even with a port.
func (u *URL) Host() string {
	h := u.Hostname()
	if h == "" || u.port == -1 {
		// Node reports "" for an empty host even with a port, as in foo://:80/.
		return h
	}
	return h + ":" + strconv.Itoa(u.port)
}

// Hostname is url.hostname: a lowercase ASCII domain (IDNA-processed), a canonical IPv4
// address, a bracketed compressed IPv6 address, or a non-special URL's opaque host.
func (u *URL) Hostname() string {
	if !u.hasHost {
		return ""
	}
	return u.host.String()
}

// Port is url.port: "" when null, which includes the scheme's default port.
func (u *URL) Port() string {
	if u.port == -1 {
		return ""
	}
	return strconv.Itoa(u.port)
}

// Pathname is url.pathname.
func (u *URL) Pathname() string { return u.pathString() }

// Search is url.search: "" for a null or empty query, otherwise '?' and the query.
func (u *URL) Search() string {
	if !u.hasQuery || u.query == "" {
		return ""
	}
	return "?" + u.query
}

// Hash is url.hash: "" for a null or empty fragment, otherwise '#' and the fragment.
func (u *URL) Hash() string {
	if !u.hasFragment || u.fragment == "" {
		return ""
	}
	return "#" + u.fragment
}

// Origin is url.origin: scheme://host[:port] for http, https, ws, wss and ftp, the inner
// http(s) URL's origin for blob:, and "null" for everything else (file: and non-special
// schemes included), as Node serializes an opaque origin.
func (u *URL) Origin() string {
	switch u.scheme {
	case "http", "https", "ws", "wss", "ftp":
		return u.scheme + "://" + u.Host()
	case "blob":
		if inner, err := Parse(u.pathString()); err == nil &&
			(inner.scheme == "http" || inner.scheme == "https") {
			return inner.scheme + "://" + inner.Host()
		}
	}
	return "null"
}

// SearchParams is url.searchParams, as a snapshot: changing it does not change u.
func (u *URL) SearchParams() *SearchParams {
	if !u.hasQuery {
		return &SearchParams{}
	}
	return parseFormURLEncoded(u.query) // unlike ParseQuery, a leading '?' is data here
}

// SetHostname is the url.hostname setter. Like Node, it leaves the URL unchanged when it
// rejects the value (it reports false then, where JavaScript reports nothing): a value with a
// port, an invalid host, an empty host on a special URL, or a URL with an opaque path.
// Anything after the host ('/', '?', '#', and '\' for special URLs) is ignored.
func (u *URL) SetHostname(value string) bool { return u.setHost(value, true) }

// SetHost is the url.host setter, which also takes a port after ':'. As in Node, a port that
// fails to parse leaves the port unchanged but still sets the host.
func (u *URL) SetHost(value string) bool { return u.setHost(value, false) }

// setHost is ada's url_aggregator::set_host_or_hostname, which Node runs for both setters.
func (u *URL) setHost(value string, hostnameOnly bool) bool {
	if u.opaque {
		return false
	}
	prevHost, prevHasHost, prevPort := u.host, u.hasHost, u.port
	if i := strings.IndexByte(value, '#'); i >= 0 {
		value = value[:i]
	}
	value = string(removeTabNewline([]rune(value)))

	if u.scheme == "file" {
		if i := strings.IndexAny(value, "/\\?"); i >= 0 {
			value = value[:i]
		}
		if value == "" {
			u.host, u.hasHost = host{}, true
			return true
		}
		h, ok := parseHost(value, true)
		if !ok {
			return false
		}
		if h.kind == hostDomain && h.name == "localhost" {
			h = host{}
		}
		u.host, u.hasHost = h, true
		return true
	}

	hostView, colon := hostDelimiter(value, u.special())
	if colon {
		if hostnameOnly {
			return false
		}
		if rest := value[len(hostView)+1:]; rest != "" {
			u.setPortFromHost(rest)
		}
	} else if hostView == "" && (u.special() || u.includesCredentials() || u.port != -1) {
		return false
	}
	if hostView == "" && !u.special() {
		if u.hasHost {
			u.host = host{}
		} else if len(u.path) > 1 && u.path[0] == "" {
			u.host, u.hasHost = host{}, true // the "/." prefix gives way to an empty host
		}
		return true
	}
	h, ok := parseHost(hostView, u.special())
	if !ok {
		u.host, u.hasHost, u.port = prevHost, prevHasHost, prevPort
		return false
	}
	u.host, u.hasHost = h, true
	return true
}

// hostDelimiter returns the part of value before the first ':', '/', '?' (or, for special URLs,
// '\\') outside brackets, and whether that delimiter was ':'.
func hostDelimiter(value string, special bool) (string, bool) {
	for i := 0; i < len(value); i++ {
		switch c := value[i]; {
		case c == '[':
			j := strings.IndexByte(value[i:], ']')
			if j < 0 {
				return value, false
			}
			i += j - 1 // resume the search at the ']'
		case c == ':':
			return value[:i], true
		case c == '/' || c == '?' || (special && c == '\\'):
			return value[:i], false
		}
	}
	return value, false
}

// setPortFromHost is ada's set_port as the host setter calls it: digits up to the first
// non-digit are the port; no leading digit clears the port; out of range keeps the old one.
func (u *URL) setPortFromHost(value string) {
	if u.scheme == "file" || !u.hasHost || u.host.String() == "" {
		return
	}
	if isC0OrSpace(rune(value[0])) || !strings.ContainsAny(value, "0123456789") || value[0] == '-' {
		return
	}
	n, port := 0, 0
	for n < len(value) && isDigit(value[n]) {
		port = port*10 + int(value[n]-'0')
		if port > 65535 {
			return
		}
		n++
	}
	if n == 0 || port == defaultPort(u.scheme) {
		u.port = -1
		return
	}
	u.port = port
}

// SetSearch is the url.search setter. url.search = "" removes the query entirely.
func (u *URL) SetSearch(value string) {
	if value == "" {
		u.query, u.hasQuery = "", false
		u.stripTrailingSpacesFromOpaquePath()
		return
	}
	value = strings.TrimPrefix(value, "?")
	c := u.Clone()
	c.query, c.hasQuery = "", true
	if r, ok := basicParse(value, nil, c, queryState); ok {
		*u = *r
	}
}

// SetHash is the url.hash setter. url.hash = "" removes the fragment entirely.
func (u *URL) SetHash(value string) {
	if value == "" {
		u.fragment, u.hasFragment = "", false
		u.stripTrailingSpacesFromOpaquePath()
		return
	}
	value = strings.TrimPrefix(value, "#")
	c := u.Clone()
	c.fragment, c.hasFragment = "", true
	if r, ok := basicParse(value, nil, c, fragmentState); ok {
		*u = *r
	}
}

func (u *URL) stripTrailingSpacesFromOpaquePath() {
	if !u.opaque || u.hasFragment || u.hasQuery {
		return
	}
	u.opaquePath = strings.TrimRight(u.opaquePath, " ")
}
