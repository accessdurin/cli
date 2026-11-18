// Package jsurl implements the subset of the WHATWG URL Standard (the JavaScript `URL` and
// `URLSearchParams` classes) that the MCP SDK port relies on: parsing with an optional base,
// serialization (href), origin, the component getters, a few setters and query parameters.
//
// INTEGRATION: internal/weburl (written in parallel) is the module-wide WHATWG URL package.
// Once it lands this package should delegate to it. Known gaps here: hosts containing
// non-ASCII characters are rejected instead of being converted with UTS #46 (IDNA), and the
// `blob:` origin rule is not implemented. Every URL the CLI handles is ASCII.
package jsurl

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// URL is a parsed WHATWG URL record.
type URL struct {
	scheme     string
	username   string
	password   string
	host       *string
	port       *int
	path       []string
	opaquePath *string
	query      *string
	fragment   *string
}

// ErrInvalidURL is returned for input the URL parser rejects (JavaScript's TypeError).
var ErrInvalidURL = errors.New("Invalid URL")

var specialPorts = map[string]int{"ftp": 21, "file": -1, "http": 80, "https": 443, "ws": 80, "wss": 443}

func isSpecial(scheme string) bool { _, ok := specialPorts[scheme]; return ok }

// Parse parses input against an optional base, like `new URL(input, base)`.
func Parse(input string, base *URL) (*URL, error) {
	u, ok := parse(input, base)
	if !ok {
		return nil, ErrInvalidURL
	}
	return u, nil
}

// ParseRef parses input against a base given as a string, like `new URL(input, base)`.
func ParseRef(input, base string) (*URL, error) {
	b, err := Parse(base, nil)
	if err != nil {
		return nil, err
	}
	return Parse(input, b)
}

// MustParse parses an absolute URL and panics on failure (for tests and constants).
func MustParse(s string) *URL {
	u, err := Parse(s, nil)
	if err != nil {
		panic(err)
	}
	return u
}

// CanParse implements URL.canParse.
func CanParse(input string) bool {
	_, ok := parse(input, nil)
	return ok
}

// Clone copies the URL (`new URL(url.href)`).
func (u *URL) Clone() *URL {
	c := *u
	c.path = append([]string(nil), u.path...)
	if u.host != nil {
		h := *u.host
		c.host = &h
	}
	if u.port != nil {
		p := *u.port
		c.port = &p
	}
	if u.opaquePath != nil {
		p := *u.opaquePath
		c.opaquePath = &p
	}
	if u.query != nil {
		q := *u.query
		c.query = &q
	}
	if u.fragment != nil {
		f := *u.fragment
		c.fragment = &f
	}
	return &c
}

func strip(input string) string {
	// Leading and trailing C0 control or space.
	start, end := 0, len(input)
	for start < end && input[start] <= 0x20 {
		start++
	}
	for end > start && input[end-1] <= 0x20 {
		end--
	}
	input = input[start:end]
	if strings.ContainsAny(input, "\t\n\r") {
		input = strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(input)
	}
	return input
}

func isASCIIAlpha(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
func isASCIIAlnum(r rune) bool { return isASCIIAlpha(r) || isASCIIDigit(r) }
func isASCIIHexDigit(r rune) bool {
	return isASCIIDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

type state int

const (
	stSchemeStart state = iota
	stScheme
	stNoScheme
	stSpecialRelativeOrAuthority
	stPathOrAuthority
	stRelative
	stRelativeSlash
	stSpecialAuthoritySlashes
	stSpecialAuthorityIgnoreSlashes
	stAuthority
	stHost
	stPort
	stFile
	stFileSlash
	stFileHost
	stPathStart
	stPath
	stOpaquePath
	stQuery
	stFragment
)

const eof = rune(-1)

func parse(raw string, base *URL) (*URL, bool) {
	input := []rune(strip(raw))
	u := &URL{}
	st := stSchemeStart
	var buf strings.Builder
	atSign, insideBrackets, passwordToken := false, false, false
	at := func(i int) rune {
		if i < len(input) && i >= 0 {
			return input[i]
		}
		return eof
	}
	for p := 0; ; p++ {
		c := at(p)
		switch st {
		case stSchemeStart:
			if isASCIIAlpha(c) {
				buf.WriteRune(toLower(c))
				st = stScheme
			} else {
				st = stNoScheme
				p--
			}
		case stScheme:
			if isASCIIAlnum(c) || c == '+' || c == '-' || c == '.' {
				buf.WriteRune(toLower(c))
			} else if c == ':' {
				u.scheme = buf.String()
				buf.Reset()
				switch {
				case u.scheme == "file":
					st = stFile
				case isSpecial(u.scheme) && base != nil && base.scheme == u.scheme:
					st = stSpecialRelativeOrAuthority
				case isSpecial(u.scheme):
					st = stSpecialAuthoritySlashes
				case at(p+1) == '/':
					st = stPathOrAuthority
					p++
				default:
					empty := ""
					u.opaquePath = &empty
					st = stOpaquePath
				}
			} else {
				buf.Reset()
				st = stNoScheme
				p = -1
			}
		case stNoScheme:
			if base == nil || (base.opaquePath != nil && c != '#') {
				return nil, false
			}
			if base.opaquePath != nil && c == '#' {
				u.scheme = base.scheme
				op := *base.opaquePath
				u.opaquePath = &op
				u.query = copyStr(base.query)
				empty := ""
				u.fragment = &empty
				st = stFragment
			} else if base.scheme != "file" {
				st = stRelative
				p--
			} else {
				st = stFile
				p--
			}
		case stSpecialRelativeOrAuthority:
			if c == '/' && at(p+1) == '/' {
				st = stSpecialAuthorityIgnoreSlashes
				p++
			} else {
				st = stRelative
				p--
			}
		case stPathOrAuthority:
			if c == '/' {
				st = stAuthority
			} else {
				st = stPath
				p--
			}
		case stRelative:
			u.scheme = base.scheme
			if c == '/' {
				st = stRelativeSlash
			} else if isSpecial(u.scheme) && c == '\\' {
				st = stRelativeSlash
			} else {
				u.username, u.password = base.username, base.password
				u.host, u.port = copyStr(base.host), copyInt(base.port)
				u.path = append([]string(nil), base.path...)
				u.query = copyStr(base.query)
				if c == '?' {
					empty := ""
					u.query = &empty
					st = stQuery
				} else if c == '#' {
					empty := ""
					u.fragment = &empty
					st = stFragment
				} else if c != eof {
					u.query = nil
					u.shortenPath()
					st = stPath
					p--
				}
			}
		case stRelativeSlash:
			if isSpecial(u.scheme) && (c == '/' || c == '\\') {
				st = stSpecialAuthorityIgnoreSlashes
			} else if c == '/' {
				st = stAuthority
			} else {
				u.username, u.password = base.username, base.password
				u.host, u.port = copyStr(base.host), copyInt(base.port)
				st = stPath
				p--
			}
		case stSpecialAuthoritySlashes:
			if c == '/' && at(p+1) == '/' {
				st = stSpecialAuthorityIgnoreSlashes
				p++
			} else {
				st = stSpecialAuthorityIgnoreSlashes
				p--
			}
		case stSpecialAuthorityIgnoreSlashes:
			if c != '/' && c != '\\' {
				st = stAuthority
				p--
			}
		case stAuthority:
			if c == '@' {
				b := buf.String()
				if atSign {
					b = "%40" + b
				}
				atSign = true
				for _, r := range b {
					if r == ':' && !passwordToken {
						passwordToken = true
						continue
					}
					enc := percentEncodeRune(r, userinfoSet)
					if passwordToken {
						u.password += enc
					} else {
						u.username += enc
					}
				}
				buf.Reset()
			} else if c == eof || c == '/' || c == '?' || c == '#' || (isSpecial(u.scheme) && c == '\\') {
				if atSign && buf.Len() == 0 {
					return nil, false
				}
				p -= utf8.RuneCountInString(buf.String()) + 1
				buf.Reset()
				st = stHost
			} else {
				buf.WriteRune(c)
			}
		case stHost:
			if c == ':' && !insideBrackets {
				if buf.Len() == 0 {
					return nil, false
				}
				h, ok := parseHost(buf.String(), !isSpecial(u.scheme))
				if !ok {
					return nil, false
				}
				u.host = &h
				buf.Reset()
				st = stPort
			} else if c == eof || c == '/' || c == '?' || c == '#' || (isSpecial(u.scheme) && c == '\\') {
				p--
				if isSpecial(u.scheme) && buf.Len() == 0 {
					return nil, false
				}
				h, ok := parseHost(buf.String(), !isSpecial(u.scheme))
				if !ok {
					return nil, false
				}
				u.host = &h
				buf.Reset()
				st = stPathStart
			} else {
				if c == '[' {
					insideBrackets = true
				}
				if c == ']' {
					insideBrackets = false
				}
				buf.WriteRune(c)
			}
		case stPort:
			if isASCIIDigit(c) {
				buf.WriteRune(c)
			} else if c == eof || c == '/' || c == '?' || c == '#' || (isSpecial(u.scheme) && c == '\\') {
				if buf.Len() != 0 {
					digits := strings.TrimLeft(buf.String(), "0")
					if len(digits) > 5 {
						return nil, false
					}
					n := 0
					if digits != "" {
						n, _ = strconv.Atoi(digits)
					}
					if n > 65535 {
						return nil, false
					}
					if def, ok := specialPorts[u.scheme]; ok && def == n {
						u.port = nil
					} else {
						u.port = &n
					}
					buf.Reset()
				}
				st = stPathStart
				p--
			} else {
				return nil, false
			}
		case stFile:
			u.scheme = "file"
			empty := ""
			u.host = &empty
			if c == '/' || c == '\\' {
				st = stFileSlash
			} else if base != nil && base.scheme == "file" {
				u.host = copyStr(base.host)
				u.path = append([]string(nil), base.path...)
				u.query = copyStr(base.query)
				if c == '?' {
					e := ""
					u.query = &e
					st = stQuery
				} else if c == '#' {
					e := ""
					u.fragment = &e
					st = stFragment
				} else if c != eof {
					u.query = nil
					if !startsWithWindowsDriveLetter(input[p:]) {
						u.shortenPath()
					} else {
						u.path = nil
					}
					st = stPath
					p--
				}
			} else {
				st = stPath
				p--
			}
		case stFileSlash:
			if c == '/' || c == '\\' {
				st = stFileHost
			} else {
				if base != nil && base.scheme == "file" {
					u.host = copyStr(base.host)
					if !startsWithWindowsDriveLetter(input[p:]) && len(base.path) > 0 && isNormalizedWindowsDriveLetter(base.path[0]) {
						u.path = append(u.path, base.path[0])
					}
				}
				st = stPath
				p--
			}
		case stFileHost:
			if c == eof || c == '/' || c == '\\' || c == '?' || c == '#' {
				p--
				if isWindowsDriveLetter(buf.String()) {
					st = stPath
				} else if buf.Len() == 0 {
					empty := ""
					u.host = &empty
					st = stPathStart
				} else {
					h, ok := parseHost(buf.String(), false)
					if !ok {
						return nil, false
					}
					if h == "localhost" {
						h = ""
					}
					u.host = &h
					buf.Reset()
					st = stPathStart
				}
			} else {
				buf.WriteRune(c)
			}
		case stPathStart:
			if isSpecial(u.scheme) {
				st = stPath
				if c != '/' && c != '\\' {
					p--
				}
			} else if c == '?' {
				e := ""
				u.query = &e
				st = stQuery
			} else if c == '#' {
				e := ""
				u.fragment = &e
				st = stFragment
			} else if c != eof {
				st = stPath
				if c != '/' {
					p--
				}
			}
		case stPath:
			special := isSpecial(u.scheme)
			if c == eof || c == '/' || (special && c == '\\') || c == '?' || c == '#' {
				seg := buf.String()
				switch {
				case isDoubleDot(seg):
					u.shortenPath()
					if c != '/' && !(special && c == '\\') {
						u.path = append(u.path, "")
					}
				case isSingleDot(seg) && c != '/' && !(special && c == '\\'):
					u.path = append(u.path, "")
				case !isSingleDot(seg):
					if u.scheme == "file" && len(u.path) == 0 && isWindowsDriveLetter(seg) {
						seg = seg[:1] + ":"
					}
					u.path = append(u.path, seg)
				}
				buf.Reset()
				if c == '?' {
					e := ""
					u.query = &e
					st = stQuery
				}
				if c == '#' {
					e := ""
					u.fragment = &e
					st = stFragment
				}
			} else {
				buf.WriteString(percentEncodeRune(c, pathSet))
			}
		case stOpaquePath:
			if c == '?' {
				e := ""
				u.query = &e
				st = stQuery
			} else if c == '#' {
				e := ""
				u.fragment = &e
				st = stFragment
			} else if c == ' ' {
				nc := at(p + 1)
				if nc == '?' || nc == '#' {
					*u.opaquePath += "%20"
				} else {
					*u.opaquePath += " "
				}
			} else if c != eof {
				*u.opaquePath += percentEncodeRune(c, c0ControlSet)
			}
		case stQuery:
			if c == '#' || c == eof {
				set := querySet
				if isSpecial(u.scheme) {
					set = specialQuerySet
				}
				*u.query += percentEncodeString(buf.String(), set)
				buf.Reset()
				if c == '#' {
					e := ""
					u.fragment = &e
					st = stFragment
				}
			} else {
				buf.WriteRune(c)
			}
		case stFragment:
			if c != eof {
				*u.fragment += percentEncodeRune(c, fragmentSet)
			}
		}
		if c == eof && p >= len(input) {
			break
		}
	}
	if u.opaquePath != nil {
		// Trailing spaces of an opaque path are stripped when there is no query or fragment.
		if u.query == nil && u.fragment == nil {
			*u.opaquePath = strings.TrimRight(*u.opaquePath, " ")
		}
	}
	return u, true
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

func copyStr(s *string) *string {
	if s == nil {
		return nil
	}
	c := *s
	return &c
}

func copyInt(i *int) *int {
	if i == nil {
		return nil
	}
	c := *i
	return &c
}

func (u *URL) shortenPath() {
	if u.scheme == "file" && len(u.path) == 1 && isNormalizedWindowsDriveLetter(u.path[0]) {
		return
	}
	if len(u.path) > 0 {
		u.path = u.path[:len(u.path)-1]
	}
}

func isSingleDot(s string) bool { return s == "." || strings.EqualFold(s, "%2e") }

func isDoubleDot(s string) bool {
	switch strings.ToLower(s) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

func isWindowsDriveLetter(s string) bool {
	return len(s) == 2 && isASCIIAlpha(rune(s[0])) && (s[1] == ':' || s[1] == '|')
}

func isNormalizedWindowsDriveLetter(s string) bool {
	return len(s) == 2 && isASCIIAlpha(rune(s[0])) && s[1] == ':'
}

func startsWithWindowsDriveLetter(rs []rune) bool {
	if len(rs) < 2 || !isASCIIAlpha(rs[0]) || (rs[1] != ':' && rs[1] != '|') {
		return false
	}
	if len(rs) == 2 {
		return true
	}
	switch rs[2] {
	case '/', '\\', '?', '#':
		return true
	}
	return false
}

// ---- percent-encoding ----

type encodeSet func(r rune) bool

func c0ControlSet(r rune) bool { return r < 0x20 || r > 0x7E }
func fragmentSet(r rune) bool {
	return c0ControlSet(r) || r == ' ' || r == '"' || r == '<' || r == '>' || r == '`'
}
func querySet(r rune) bool {
	return c0ControlSet(r) || r == ' ' || r == '"' || r == '#' || r == '<' || r == '>'
}
func specialQuerySet(r rune) bool { return querySet(r) || r == '\'' }
func pathSet(r rune) bool {
	// Node's URL implementation (ada) does not encode '^' in paths.
	return querySet(r) || r == '?' || r == '`' || r == '{' || r == '}'
}
func userinfoSet(r rune) bool {
	return pathSet(r) || r == '/' || r == ':' || r == ';' || r == '=' || r == '@' || (r >= '[' && r <= '^') || r == '|'
}
func componentSet(r rune) bool {
	return userinfoSet(r) || (r >= '$' && r <= '&') || r == '+' || r == ','
}
func urlencodedSet(r rune) bool {
	return componentSet(r) || r == '!' || (r >= '\'' && r <= ')') || r == '~'
}

const upperHex = "0123456789ABCDEF"

func percentEncodeRune(r rune, set encodeSet) string {
	if !set(r) {
		return string(r)
	}
	if r >= 0xD800 && r <= 0xDFFF {
		r = utf8.RuneError
	}
	var b [4]byte
	n := utf8.EncodeRune(b[:], r)
	var sb strings.Builder
	for _, x := range b[:n] {
		sb.WriteByte('%')
		sb.WriteByte(upperHex[x>>4])
		sb.WriteByte(upperHex[x&0xF])
	}
	return sb.String()
}

func percentEncodeString(s string, set encodeSet) string {
	var sb strings.Builder
	for _, r := range s {
		sb.WriteString(percentEncodeRune(r, set))
	}
	return sb.String()
}

func percentDecode(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isASCIIHexDigit(rune(s[i+1])) && isASCIIHexDigit(rune(s[i+2])) {
			v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
			out = append(out, byte(v))
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return out
}

func decodeUTF8Lossy(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		sb.WriteRune(r)
		b = b[size:]
	}
	return sb.String()
}

// ---- hosts ----

func isForbiddenHostCodePoint(r rune) bool {
	switch r {
	case 0, '\t', '\n', '\r', ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|':
		return true
	}
	return false
}

func isForbiddenDomainCodePoint(r rune) bool {
	return isForbiddenHostCodePoint(r) || (r >= 0 && r <= 0x1F) || r == '%' || r == 0x7F
}

func parseHost(input string, notSpecial bool) (string, bool) {
	if strings.HasPrefix(input, "[") {
		if !strings.HasSuffix(input, "]") {
			return "", false
		}
		addr, ok := parseIPv6(input[1 : len(input)-1])
		if !ok {
			return "", false
		}
		return "[" + serializeIPv6(addr) + "]", true
	}
	if notSpecial {
		for _, r := range input {
			if isForbiddenHostCodePoint(r) {
				return "", false
			}
		}
		return percentEncodeString(input, c0ControlSet), true
	}
	domain := decodeUTF8Lossy(percentDecode(input))
	// domain to ASCII: only the ASCII path of UTS #46 is implemented (lowercasing).
	var sb strings.Builder
	for _, r := range domain {
		if r >= utf8.RuneSelf {
			return "", false
		}
		sb.WriteRune(toLower(r))
	}
	ascii := sb.String()
	if ascii == "" {
		return "", false
	}
	for _, r := range ascii {
		if isForbiddenDomainCodePoint(r) {
			return "", false
		}
	}
	if endsInANumber(ascii) {
		v, ok := parseIPv4(ascii)
		if !ok {
			return "", false
		}
		return serializeIPv4(v), true
	}
	return ascii, true
}

func endsInANumber(s string) bool {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := parseIPv4Number(last)
	return ok
}

func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	radix := 10
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
		radix = 16
	} else if len(s) >= 2 && s[0] == '0' {
		s = s[1:]
		radix = 8
	}
	if s == "" {
		return 0, true
	}
	for _, r := range s {
		ok := false
		switch radix {
		case 10:
			ok = isASCIIDigit(r)
		case 16:
			ok = isASCIIHexDigit(r)
		case 8:
			ok = r >= '0' && r <= '7'
		}
		if !ok {
			return 0, false
		}
	}
	// Values beyond 2^64 still mean "a number"; saturate.
	v, err := strconv.ParseUint(s, radix, 64)
	if err != nil {
		return 1 << 63, true
	}
	return v, true
}

func parseIPv4(s string) (uint32, bool) {
	parts := strings.Split(s, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return 0, false
	}
	nums := make([]uint64, 0, len(parts))
	for _, p := range parts {
		n, ok := parseIPv4Number(p)
		if !ok {
			return 0, false
		}
		nums = append(nums, n)
	}
	for _, n := range nums[:len(nums)-1] {
		if n > 255 {
			return 0, false
		}
	}
	last := nums[len(nums)-1]
	limit := uint64(1) << (8 * (5 - len(nums)))
	if last >= limit {
		return 0, false
	}
	ipv4 := last
	for i, n := range nums[:len(nums)-1] {
		ipv4 += n << (8 * (3 - i))
	}
	return uint32(ipv4), true
}

func serializeIPv4(v uint32) string {
	return strconv.Itoa(int(v>>24)) + "." + strconv.Itoa(int(v>>16&0xFF)) + "." + strconv.Itoa(int(v>>8&0xFF)) + "." + strconv.Itoa(int(v&0xFF))
}

func parseIPv6(s string) ([8]uint16, bool) {
	var addr [8]uint16
	in := []rune(s)
	pieceIndex, compress := 0, -1
	p := 0
	at := func(i int) rune {
		if i < len(in) {
			return in[i]
		}
		return eof
	}
	if at(p) == ':' {
		if at(p+1) != ':' {
			return addr, false
		}
		p += 2
		pieceIndex++
		compress = pieceIndex
	}
	for at(p) != eof {
		if pieceIndex == 8 {
			return addr, false
		}
		if at(p) == ':' {
			if compress != -1 {
				return addr, false
			}
			p++
			pieceIndex++
			compress = pieceIndex
			continue
		}
		value, length := 0, 0
		for length < 4 && isASCIIHexDigit(at(p)) {
			h, _ := strconv.ParseUint(string(at(p)), 16, 8)
			value = value*0x10 + int(h)
			p++
			length++
		}
		if at(p) == '.' {
			if length == 0 {
				return addr, false
			}
			p -= length
			if pieceIndex > 6 {
				return addr, false
			}
			numbersSeen := 0
			for at(p) != eof {
				ipv4Piece := -1
				if numbersSeen > 0 {
					if at(p) == '.' && numbersSeen < 4 {
						p++
					} else {
						return addr, false
					}
				}
				if !isASCIIDigit(at(p)) {
					return addr, false
				}
				for isASCIIDigit(at(p)) {
					n := int(at(p) - '0')
					if ipv4Piece == -1 {
						ipv4Piece = n
					} else if ipv4Piece == 0 {
						return addr, false
					} else {
						ipv4Piece = ipv4Piece*10 + n
					}
					if ipv4Piece > 255 {
						return addr, false
					}
					p++
				}
				addr[pieceIndex] = addr[pieceIndex]*0x100 + uint16(ipv4Piece)
				numbersSeen++
				if numbersSeen == 2 || numbersSeen == 4 {
					pieceIndex++
				}
			}
			if numbersSeen != 4 {
				return addr, false
			}
			break
		} else if at(p) == ':' {
			p++
			if at(p) == eof {
				return addr, false
			}
		} else if at(p) != eof {
			return addr, false
		}
		addr[pieceIndex] = uint16(value)
		pieceIndex++
	}
	if compress != -1 {
		swaps := pieceIndex - compress
		pieceIndex = 7
		for pieceIndex != 0 && swaps > 0 {
			addr[pieceIndex], addr[compress+swaps-1] = addr[compress+swaps-1], addr[pieceIndex]
			pieceIndex--
			swaps--
		}
	} else if pieceIndex != 8 {
		return addr, false
	}
	return addr, true
}

func serializeIPv6(addr [8]uint16) string {
	// Find the first longest run of two or more zero pieces.
	best, bestLen := -1, 1
	for i := 0; i < 8; {
		if addr[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && addr[j] == 0 {
			j++
		}
		if j-i > bestLen {
			best, bestLen = i, j-i
		}
		i = j
	}
	var sb strings.Builder
	ignore0 := false
	for i := 0; i < 8; i++ {
		if ignore0 && addr[i] == 0 {
			continue
		}
		ignore0 = false
		if best == i {
			if i == 0 {
				sb.WriteString("::")
			} else {
				sb.WriteString(":")
			}
			ignore0 = true
			continue
		}
		sb.WriteString(strconv.FormatUint(uint64(addr[i]), 16))
		if i != 7 {
			sb.WriteString(":")
		}
	}
	return sb.String()
}

// ---- getters ----

// Href serializes the URL.
func (u *URL) Href() string {
	var sb strings.Builder
	sb.WriteString(u.scheme)
	sb.WriteByte(':')
	if u.host != nil {
		sb.WriteString("//")
		if u.username != "" || u.password != "" {
			sb.WriteString(u.username)
			if u.password != "" {
				sb.WriteByte(':')
				sb.WriteString(u.password)
			}
			sb.WriteByte('@')
		}
		sb.WriteString(*u.host)
		if u.port != nil {
			sb.WriteByte(':')
			sb.WriteString(strconv.Itoa(*u.port))
		}
	}
	if u.host == nil && u.opaquePath == nil && len(u.path) > 1 && u.path[0] == "" {
		sb.WriteString("/.")
	}
	sb.WriteString(u.Pathname())
	if u.query != nil {
		sb.WriteByte('?')
		sb.WriteString(*u.query)
	}
	if u.fragment != nil {
		sb.WriteByte('#')
		sb.WriteString(*u.fragment)
	}
	return sb.String()
}

// String returns Href, so a *URL formats like String(url) in JavaScript.
func (u *URL) String() string { return u.Href() }

// Protocol returns the scheme followed by ':'.
func (u *URL) Protocol() string { return u.scheme + ":" }

// Username returns the percent-encoded username.
func (u *URL) Username() string { return u.username }

// Password returns the percent-encoded password.
func (u *URL) Password() string { return u.password }

// Hostname returns the serialized host without port.
func (u *URL) Hostname() string {
	if u.host == nil {
		return ""
	}
	return *u.host
}

// Port returns the port, or "" when absent or default.
func (u *URL) Port() string {
	if u.port == nil {
		return ""
	}
	return strconv.Itoa(*u.port)
}

// Host returns hostname[:port].
func (u *URL) Host() string {
	if u.host == nil {
		return ""
	}
	if u.port == nil {
		return *u.host
	}
	return *u.host + ":" + strconv.Itoa(*u.port)
}

// Pathname returns the serialized path.
func (u *URL) Pathname() string {
	if u.opaquePath != nil {
		return *u.opaquePath
	}
	var sb strings.Builder
	for _, seg := range u.path {
		sb.WriteByte('/')
		sb.WriteString(seg)
	}
	return sb.String()
}

// Search returns "?query" or "" when the query is null or empty.
func (u *URL) Search() string {
	if u.query == nil || *u.query == "" {
		return ""
	}
	return "?" + *u.query
}

// Hash returns "#fragment" or "" when the fragment is null or empty.
func (u *URL) Hash() string {
	if u.fragment == nil || *u.fragment == "" {
		return ""
	}
	return "#" + *u.fragment
}

// Origin returns the ASCII serialization of the URL's origin.
func (u *URL) Origin() string {
	switch u.scheme {
	case "http", "https", "ws", "wss", "ftp":
		return u.scheme + "://" + u.Host()
	}
	return "null"
}

// ---- setters ----

// SetSearch implements the `search` setter.
func (u *URL) SetSearch(v string) {
	if v == "" {
		u.query = nil
		return
	}
	v = strings.TrimPrefix(v, "?")
	set := querySet
	if isSpecial(u.scheme) {
		set = specialQuerySet
	}
	q := percentEncodeString(strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v), set)
	u.query = &q
}

// SetHash implements the `hash` setter.
func (u *URL) SetHash(v string) {
	if v == "" {
		u.fragment = nil
		return
	}
	v = strings.TrimPrefix(v, "#")
	f := percentEncodeString(strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(v), fragmentSet)
	u.fragment = &f
}

func (u *URL) cannotHaveCredentialsOrPort() bool {
	return u.host == nil || *u.host == "" || u.scheme == "file"
}

// SetUsername implements the `username` setter.
func (u *URL) SetUsername(v string) {
	if u.cannotHaveCredentialsOrPort() {
		return
	}
	u.username = percentEncodeString(v, userinfoSet)
}

// SetPassword implements the `password` setter.
func (u *URL) SetPassword(v string) {
	if u.cannotHaveCredentialsOrPort() {
		return
	}
	u.password = percentEncodeString(v, userinfoSet)
}

// SetProtocol implements the `protocol` setter for special-to-special scheme changes.
func (u *URL) SetProtocol(v string) {
	v = strings.TrimSuffix(strings.ToLower(v), ":")
	if !isSpecial(v) || !isSpecial(u.scheme) || v == "file" || u.scheme == "file" {
		return
	}
	u.scheme = v
	if u.port != nil {
		if def, ok := specialPorts[v]; ok && def == *u.port {
			u.port = nil
		}
	}
}

// ---- URLSearchParams ----

// SearchParams is a view of the URL's query, like url.searchParams.
type SearchParams struct {
	list [][2]string
	url  *URL
}

// ParseQuery parses an application/x-www-form-urlencoded string.
func ParseQuery(q string) [][2]string {
	var out [][2]string
	for _, seq := range strings.Split(q, "&") {
		if seq == "" {
			continue
		}
		name, value, _ := strings.Cut(seq, "=")
		name = strings.ReplaceAll(name, "+", " ")
		value = strings.ReplaceAll(value, "+", " ")
		out = append(out, [2]string{decodeUTF8Lossy(percentDecode(name)), decodeUTF8Lossy(percentDecode(value))})
	}
	return out
}

// SerializeQuery serializes name/value pairs with the application/x-www-form-urlencoded
// serializer (URLSearchParams#toString).
func SerializeQuery(list [][2]string) string {
	var sb strings.Builder
	for i, kv := range list {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(formEncode(kv[0]))
		sb.WriteByte('=')
		sb.WriteString(formEncode(kv[1]))
	}
	return sb.String()
}

func formEncode(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r == ' ' {
			sb.WriteByte('+')
			continue
		}
		sb.WriteString(percentEncodeRune(r, urlencodedSet))
	}
	return sb.String()
}

// SearchParams returns the query parameters bound to u.
func (u *URL) SearchParams() *SearchParams {
	sp := &SearchParams{url: u}
	if u.query != nil {
		sp.list = ParseQuery(*u.query)
	}
	return sp
}

func (sp *SearchParams) update() {
	if sp.url == nil {
		return
	}
	s := SerializeQuery(sp.list)
	if s == "" {
		sp.url.query = nil
		return
	}
	sp.url.query = &s
}

// Get returns the first value for name.
func (sp *SearchParams) Get(name string) (string, bool) {
	for _, kv := range sp.list {
		if kv[0] == name {
			return kv[1], true
		}
	}
	return "", false
}

// Set replaces all values for name with one value, keeping the first one's position.
func (sp *SearchParams) Set(name, value string) {
	found := false
	out := sp.list[:0:0]
	for _, kv := range sp.list {
		if kv[0] == name {
			if found {
				continue
			}
			found = true
			kv[1] = value
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, [2]string{name, value})
	}
	sp.list = out
	sp.update()
}

// Append adds a value.
func (sp *SearchParams) Append(name, value string) {
	sp.list = append(sp.list, [2]string{name, value})
	sp.update()
}

// Pairs returns the name/value list.
func (sp *SearchParams) Pairs() [][2]string { return append([][2]string(nil), sp.list...) }

// String serializes the parameters.
func (sp *SearchParams) String() string { return SerializeQuery(sp.list) }
