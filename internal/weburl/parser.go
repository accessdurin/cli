package weburl

import (
	"strings"
	"unicode/utf8"
)

type state uint8

const (
	noOverride state = iota
	schemeStartState
	schemeState
	noSchemeState
	specialRelativeOrAuthorityState
	pathOrAuthorityState
	relativeState
	relativeSlashState
	specialAuthoritySlashesState
	specialAuthorityIgnoreSlashesState
	authorityState
	hostState
	portState
	fileState
	fileSlashState
	fileHostState
	pathStartState
	pathState
	opaquePathState
	queryState
	fragmentState
)

const eof rune = -1

var specialSchemes = map[string]int{
	"ftp": 21, "file": -1, "http": 80, "https": 443, "ws": 80, "wss": 443,
}

func isSpecialScheme(s string) bool { _, ok := specialSchemes[s]; return ok }

func defaultPort(scheme string) int {
	if p, ok := specialSchemes[scheme]; ok {
		return p
	}
	return -1
}

func isASCIIAlpha(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isASCIIAlnum(c rune) bool { return isASCIIAlpha(c) || (c >= '0' && c <= '9') }

func lowerRune(c rune) rune {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

func isWindowsDriveLetter(s []rune) bool {
	return len(s) == 2 && isASCIIAlpha(s[0]) && (s[1] == ':' || s[1] == '|')
}

func isNormalizedWindowsDriveLetter(s string) bool {
	return len(s) == 2 && isASCIIAlpha(rune(s[0])) && s[1] == ':'
}

func startsWithWindowsDriveLetter(s []rune) bool {
	if len(s) < 2 || !isWindowsDriveLetter(s[:2]) {
		return false
	}
	if len(s) == 2 {
		return true
	}
	switch s[2] {
	case '/', '\\', '?', '#':
		return true
	}
	return false
}

func isSingleDot(s string) bool { return s == "." || strings.EqualFold(s, "%2e") }

func isDoubleDot(s string) bool {
	switch strings.ToLower(s) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

func appendRuneEncoded(b *strings.Builder, c rune, set encodeSet) {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], c)
	appendPercentEncoded(b, string(buf[:n]), set)
}

// shortenPath removes the last path segment and reports whether it removed one. On an opaque
// path, which only a relative reference against an opaque base reaches (see noSchemeState), it
// cuts at the last '/', as ada does with its string representation of the path.
func (u *URL) shortenPath() bool {
	if u.opaque {
		i := strings.LastIndexByte(u.opaquePath, '/')
		if i < 0 {
			return false
		}
		u.opaquePath = u.opaquePath[:i]
		return true
	}
	if u.scheme == "file" && len(u.path) == 1 && isNormalizedWindowsDriveLetter(u.path[0]) {
		return false
	}
	if len(u.path) == 0 {
		return false
	}
	u.path = u.path[:len(u.path)-1]
	return true
}

func (u *URL) appendSegment(seg string) {
	if u.opaque {
		u.opaquePath += "/" + seg
		return
	}
	u.path = append(u.path, seg)
}

func isC0OrSpace(c rune) bool { return c >= 0 && c <= 0x20 }

func removeTabNewline(in []rune) []rune {
	out := in[:0:0]
	for _, c := range in {
		if c != '\t' && c != '\n' && c != '\r' {
			out = append(out, c)
		}
	}
	return out
}

// basicParse is the URL Standard's basic URL parser, with ada's deviations where Node's
// output shows them. With override set (only queryState or fragmentState, for the search and
// hash setters) it updates url in place from that state; callers pass a copy so that a
// failure leaves the original untouched. The host setters follow ada instead (URL.setHost).
func basicParse(input string, base *URL, url *URL, override state) (*URL, bool) {
	in := []rune(input)
	if url == nil {
		url = &URL{port: -1}
		start, end := 0, len(in)
		for start < end && isC0OrSpace(in[start]) {
			start++
		}
		for end > start && isC0OrSpace(in[end-1]) {
			end--
		}
		in = in[start:end]
	}
	in = removeTabNewline(in)

	st := override
	if st == noOverride {
		st = schemeStartState
	}
	hasFragment := false
	for _, c := range in {
		hasFragment = hasFragment || c == '#'
	}
	var buffer strings.Builder
	var runeBuf []rune // the authority and host states need code points, not bytes
	atSignSeen, insideBrackets, passwordTokenSeen := false, false, false

	at := func(i int) rune {
		if i >= 0 && i < len(in) {
			return in[i]
		}
		return eof
	}
	remainingStartsWith := func(p int, s string) bool {
		r := []rune(s)
		if p+1+len(r) > len(in) {
			return false
		}
		for i, c := range r {
			if in[p+1+i] != c {
				return false
			}
		}
		return true
	}

	for p := 0; ; p++ {
		c := at(p)
		switch st {
		case schemeStartState:
			if isASCIIAlpha(c) {
				buffer.WriteRune(lowerRune(c))
				st = schemeState
			} else {
				st = noSchemeState
				p--
			}

		case schemeState:
			if isASCIIAlnum(c) || c == '+' || c == '-' || c == '.' {
				buffer.WriteRune(lowerRune(c))
			} else if c == ':' {
				url.scheme = buffer.String()
				buffer.Reset()
				switch {
				case url.scheme == "file":
					st = fileState
				case url.special() && base != nil && base.scheme == url.scheme:
					st = specialRelativeOrAuthorityState
				case url.special():
					st = specialAuthoritySlashesState
				case remainingStartsWith(p, "/"):
					st = pathOrAuthorityState
					p++
				default:
					url.opaque = true
					url.opaquePath = ""
					st = opaquePathState
				}
			} else {
				buffer.Reset()
				st = noSchemeState
				p = -1
			}

		case noSchemeState:
			// ada splits the fragment off first and fails here only when the input has no
			// fragment at all, so "/x#f" against "foo:opaque" resolves (to "foo:/x#f") where the
			// standard fails; relativeState then works on the opaque path as a string.
			if base == nil || (base.opaque && !hasFragment) {
				return nil, false
			} else if base.opaque && c == '#' {
				url.scheme = base.scheme
				url.opaque, url.opaquePath = true, base.opaquePath
				url.query, url.hasQuery = base.query, base.hasQuery
				url.fragment, url.hasFragment = "", true
				st = fragmentState
			} else if base.scheme != "file" {
				st = relativeState
				p--
			} else {
				st = fileState
				p--
			}

		case specialRelativeOrAuthorityState:
			if c == '/' && remainingStartsWith(p, "/") {
				st = specialAuthorityIgnoreSlashesState
				p++
			} else {
				st = relativeState
				p--
			}

		case pathOrAuthorityState:
			if c == '/' {
				st = authorityState
			} else {
				st = pathState
				p--
			}

		case relativeState:
			url.scheme = base.scheme
			if c == '/' || (url.special() && c == '\\') {
				st = relativeSlashState
			} else {
				url.username, url.password = base.username, base.password
				url.host, url.hasHost, url.port = base.host, base.hasHost, base.port
				url.path = append([]string(nil), base.path...)
				url.opaque, url.opaquePath = base.opaque, base.opaquePath
				url.query, url.hasQuery = base.query, base.hasQuery
				switch {
				case c == '?':
					url.query, url.hasQuery = "", true
					st = queryState
				case c == '#':
					url.fragment, url.hasFragment = "", true
					st = fragmentState
				case c != eof:
					url.query, url.hasQuery = "", false
					url.shortenPath()
					st = pathState
					p--
				}
			}

		case relativeSlashState:
			if url.special() && (c == '/' || c == '\\') {
				st = specialAuthorityIgnoreSlashesState
			} else if c == '/' {
				st = authorityState
			} else {
				url.username, url.password = base.username, base.password
				url.host, url.hasHost, url.port = base.host, base.hasHost, base.port
				st = pathState
				p--
			}

		case specialAuthoritySlashesState:
			st = specialAuthorityIgnoreSlashesState
			if c == '/' && remainingStartsWith(p, "/") {
				p++
			} else {
				p--
			}

		case specialAuthorityIgnoreSlashesState:
			if c != '/' && c != '\\' {
				st = authorityState
				p--
			}

		case authorityState:
			if c == '@' {
				if atSignSeen {
					runeBuf = append([]rune("%40"), runeBuf...)
				}
				atSignSeen = true
				var user, pass strings.Builder
				user.WriteString(url.username)
				pass.WriteString(url.password)
				for _, cp := range runeBuf {
					if cp == ':' && !passwordTokenSeen {
						passwordTokenSeen = true
						continue
					}
					if passwordTokenSeen {
						appendRuneEncoded(&pass, cp, userinfoSet)
					} else {
						appendRuneEncoded(&user, cp, userinfoSet)
					}
				}
				url.username, url.password = user.String(), pass.String()
				runeBuf = runeBuf[:0]
			} else if c == eof || c == '/' || c == '?' || c == '#' || (url.special() && c == '\\') {
				if atSignSeen && len(runeBuf) == 0 {
					return nil, false
				}
				p -= len(runeBuf) + 1
				runeBuf = runeBuf[:0]
				st = hostState
			} else {
				runeBuf = append(runeBuf, c)
			}

		case hostState:
			if c == ':' && !insideBrackets {
				if len(runeBuf) == 0 {
					return nil, false
				}
				h, ok := parseHost(string(runeBuf), url.special())
				if !ok {
					return nil, false
				}
				url.host, url.hasHost = h, true
				runeBuf = runeBuf[:0]
				st = portState
			} else if c == eof || c == '/' || c == '?' || c == '#' || (url.special() && c == '\\') {
				p--
				if url.special() && len(runeBuf) == 0 {
					return nil, false
				}
				h, ok := parseHost(string(runeBuf), url.special())
				if len(runeBuf) == 0 {
					h, ok = host{}, true // the empty host
				}
				if !ok {
					return nil, false
				}
				url.host, url.hasHost = h, true
				runeBuf = runeBuf[:0]
				st = pathStartState
			} else {
				if c == '[' {
					insideBrackets = true
				}
				if c == ']' {
					insideBrackets = false
				}
				runeBuf = append(runeBuf, c)
			}

		case portState:
			if c >= '0' && c <= '9' {
				runeBuf = append(runeBuf, c)
			} else if c == eof || c == '/' || c == '?' || c == '#' || (url.special() && c == '\\') {
				if len(runeBuf) > 0 {
					port := 0
					for _, d := range runeBuf {
						port = port*10 + int(d-'0')
						if port > 65535 {
							return nil, false
						}
					}
					if port == defaultPort(url.scheme) {
						port = -1
					}
					url.port = port
					runeBuf = runeBuf[:0]
				}
				st = pathStartState
				p--
			} else {
				return nil, false
			}

		case fileState:
			url.scheme = "file"
			url.host, url.hasHost = host{}, true
			if c == '/' || c == '\\' {
				st = fileSlashState
			} else if base != nil && base.scheme == "file" {
				url.host, url.hasHost = base.host, base.hasHost
				url.path = append([]string(nil), base.path...)
				url.query, url.hasQuery = base.query, base.hasQuery
				switch {
				case c == '?':
					url.query, url.hasQuery = "", true
					st = queryState
				case c == '#':
					url.fragment, url.hasFragment = "", true
					st = fragmentState
				case c != eof:
					url.query, url.hasQuery = "", false
					if !startsWithWindowsDriveLetter(in[p:]) {
						url.shortenPath()
					} else {
						url.path = nil
					}
					st = pathState
					p--
				}
			} else {
				st = pathState
				p--
			}

		case fileSlashState:
			if c == '/' || c == '\\' {
				st = fileHostState
			} else {
				if base != nil && base.scheme == "file" {
					url.host, url.hasHost = base.host, base.hasHost
					if !startsWithWindowsDriveLetter(in[p:]) && len(base.path) > 0 &&
						isNormalizedWindowsDriveLetter(base.path[0]) {
						url.path = append(url.path, base.path[0])
					}
				}
				st = pathState
				p--
			}

		case fileHostState:
			if c == eof || c == '/' || c == '\\' || c == '?' || c == '#' {
				p--
				if isWindowsDriveLetter(runeBuf) {
					// The drive letter quirk: buffer carries over into the path state.
					buffer.WriteString(string(runeBuf))
					runeBuf = runeBuf[:0]
					st = pathState
				} else if len(runeBuf) == 0 {
					url.host, url.hasHost = host{}, true
					st = pathStartState
				} else {
					h, ok := parseHost(string(runeBuf), url.special())
					if !ok {
						return nil, false
					}
					if h.kind == hostDomain && h.name == "localhost" {
						h = host{}
					}
					url.host, url.hasHost = h, true
					runeBuf = runeBuf[:0]
					st = pathStartState
				}
			} else {
				runeBuf = append(runeBuf, c)
			}

		case pathStartState:
			if url.special() {
				st = pathState
				if c != '/' && c != '\\' {
					p--
				}
			} else if c == '?' {
				url.query, url.hasQuery = "", true
				st = queryState
			} else if c == '#' {
				url.fragment, url.hasFragment = "", true
				st = fragmentState
			} else if c != eof {
				st = pathState
				if c != '/' {
					p--
				}
			}

		case pathState:
			slash := c == '/' || (url.special() && c == '\\')
			if c == eof || slash || c == '?' || c == '#' {
				seg := buffer.String()
				switch {
				case isDoubleDot(seg):
					// ada appends the empty segment after a final ".." only when shortening
					// removed a segment or the URL is special: new URL("mcp://x/..") is "mcp://x".
					if url.shortenPath() || url.special() {
						if !slash {
							url.appendSegment("")
						}
					}
				case isSingleDot(seg) && !slash:
					url.appendSegment("")
				case !isSingleDot(seg):
					if url.scheme == "file" && !url.opaque && len(url.path) == 0 &&
						isWindowsDriveLetter([]rune(seg)) {
						seg = seg[:1] + ":"
					}
					url.appendSegment(seg)
				}
				buffer.Reset()
				if c == '?' {
					url.query, url.hasQuery = "", true
					st = queryState
				}
				if c == '#' {
					url.fragment, url.hasFragment = "", true
					st = fragmentState
				}
			} else {
				appendRuneEncoded(&buffer, c, pathSet)
			}

		case opaquePathState:
			switch {
			case c == '?':
				url.query, url.hasQuery = "", true
				st = queryState
			case c == '#':
				url.fragment, url.hasFragment = "", true
				st = fragmentState
			case c != eof:
				var b strings.Builder
				b.WriteString(url.opaquePath)
				appendRuneEncoded(&b, c, c0ControlSet)
				url.opaquePath = b.String()
			}

		case queryState:
			if (override == noOverride && c == '#') || c == eof {
				set := querySet
				if url.special() {
					set = specialQuerySet
				}
				var b strings.Builder
				b.WriteString(url.query)
				appendPercentEncoded(&b, buffer.String(), set)
				url.query = b.String()
				buffer.Reset()
				if c == '#' {
					url.fragment, url.hasFragment = "", true
					st = fragmentState
				}
			} else {
				buffer.WriteRune(c)
			}

		case fragmentState:
			if c != eof {
				var b strings.Builder
				b.WriteString(url.fragment)
				appendRuneEncoded(&b, c, fragmentSet)
				url.fragment = b.String()
			}
		}
		if p >= len(in) {
			break
		}
	}
	return url, true
}
