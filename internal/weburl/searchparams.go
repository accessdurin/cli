package weburl

import (
	"strings"
	"unicode/utf8"
)

// SearchParams is an ordered list of name-value pairs with URLSearchParams semantics.
type SearchParams struct {
	list [][2]string
}

// ParseQuery is new URLSearchParams(query): one leading '?' is dropped, then the rest is
// parsed as application/x-www-form-urlencoded ('+' is a space, %XX sequences are decoded,
// and invalid UTF-8 after decoding becomes U+FFFD).
func ParseQuery(query string) *SearchParams {
	return parseFormURLEncoded(strings.TrimPrefix(query, "?"))
}

// NewSearchParams is new URLSearchParams(pairs) for a sequence of pairs. A JavaScript record
// argument ({a: '1'}) is the same as its entries in property order; note that JavaScript
// orders integer-like keys ("0", "1") first.
func NewSearchParams(pairs ...[2]string) *SearchParams {
	p := &SearchParams{}
	for _, kv := range pairs {
		p.Append(kv[0], kv[1])
	}
	return p
}

func parseFormURLEncoded(input string) *SearchParams {
	p := &SearchParams{}
	for _, seq := range strings.Split(usv(input), "&") {
		if seq == "" {
			continue
		}
		name, value, _ := strings.Cut(seq, "=")
		p.list = append(p.list, [2]string{formDecode(name), formDecode(value)})
	}
	return p
}

// usv reads s as a USVString: each invalid UTF-8 byte becomes U+FFFD.
func usv(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return string([]rune(s))
}

func formDecode(s string) string {
	return decodeUTF8(percentDecode(strings.ReplaceAll(s, "+", " ")))
}

// decodeUTF8 is the Encoding Standard's UTF-8 decode: each maximal invalid subpart becomes
// one U+FFFD, which is what JavaScript (and so URLSearchParams) produces.
func decodeUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var out strings.Builder
	var cp rune
	needed, seen := 0, 0
	lower, upper := byte(0x80), byte(0xBF)
	for i := 0; i < len(s); {
		c := s[i]
		if needed == 0 {
			switch {
			case c <= 0x7f:
				out.WriteByte(c)
			case c >= 0xc2 && c <= 0xdf:
				needed, cp = 1, rune(c&0x1f)
			case c >= 0xe0 && c <= 0xef:
				if c == 0xe0 {
					lower = 0xa0
				}
				if c == 0xed {
					upper = 0x9f
				}
				needed, cp = 2, rune(c&0xf)
			case c >= 0xf0 && c <= 0xf4:
				if c == 0xf0 {
					lower = 0x90
				}
				if c == 0xf4 {
					upper = 0x8f
				}
				needed, cp = 3, rune(c&0x7)
			default:
				out.WriteRune(utf8.RuneError)
			}
			i++
			continue
		}
		if c < lower || c > upper {
			cp, needed, seen = 0, 0, 0
			lower, upper = 0x80, 0xbf
			out.WriteRune(utf8.RuneError)
			continue // the byte starts the next sequence
		}
		lower, upper = 0x80, 0xbf
		cp = cp<<6 | rune(c&0x3f)
		seen++
		i++
		if seen == needed {
			out.WriteRune(cp)
			cp, needed, seen = 0, 0, 0
		}
	}
	if needed != 0 {
		out.WriteRune(utf8.RuneError)
	}
	return out.String()
}

// Append is params.append(name, value).
func (p *SearchParams) Append(name, value string) {
	p.list = append(p.list, [2]string{usv(name), usv(value)})
}

// Set is params.set(name, value): it replaces the first pair named name and removes the rest,
// or appends one.
func (p *SearchParams) Set(name, value string) {
	name, value = usv(name), usv(value)
	out := p.list[:0]
	found := false
	for _, kv := range p.list {
		if kv[0] != name {
			out = append(out, kv)
		} else if !found {
			found = true
			out = append(out, [2]string{name, value})
		}
	}
	if !found {
		out = append(out, [2]string{name, value})
	}
	p.list = out
}

// Delete is params.delete(name).
func (p *SearchParams) Delete(name string) {
	out := p.list[:0]
	for _, kv := range p.list {
		if kv[0] != name {
			out = append(out, kv)
		}
	}
	p.list = out
}

// Get is params.get(name); ok is false where JavaScript returns null.
func (p *SearchParams) Get(name string) (value string, ok bool) {
	for _, kv := range p.list {
		if kv[0] == name {
			return kv[1], true
		}
	}
	return "", false
}

// GetAll is params.getAll(name).
func (p *SearchParams) GetAll(name string) []string {
	var out []string
	for _, kv := range p.list {
		if kv[0] == name {
			out = append(out, kv[1])
		}
	}
	return out
}

// Has is params.has(name).
func (p *SearchParams) Has(name string) bool {
	_, ok := p.Get(name)
	return ok
}

// Size is params.size.
func (p *SearchParams) Size() int { return len(p.list) }

// Entries is [...params]: every pair, in order.
func (p *SearchParams) Entries() [][2]string {
	return append([][2]string(nil), p.list...)
}

// Object is Object.fromEntries(params): when a name repeats, the last value wins.
func (p *SearchParams) Object() map[string]string {
	out := make(map[string]string, len(p.list))
	for _, kv := range p.list {
		out[kv[0]] = kv[1]
	}
	return out
}

// String is params.toString(): application/x-www-form-urlencoded, with spaces as '+' and
// everything but ASCII alphanumerics and *-._ percent-encoded.
func (p *SearchParams) String() string {
	var b strings.Builder
	for i, kv := range p.list {
		if i > 0 {
			b.WriteByte('&')
		}
		formEncode(&b, kv[0])
		b.WriteByte('=')
		formEncode(&b, kv[1])
	}
	return b.String()
}

func formEncode(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ':
			b.WriteByte('+')
		case formURLEncodedSet(c):
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
}
