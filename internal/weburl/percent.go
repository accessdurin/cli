package weburl

import "strings"

// encodeSet reports whether an ASCII byte is in a percent-encode set. Bytes 0x80 and above,
// which only occur inside UTF-8 sequences, are in every set.
type encodeSet func(c byte) bool

func c0ControlSet(c byte) bool { return c < 0x20 || c > 0x7e }

func fragmentSet(c byte) bool {
	return c0ControlSet(c) || c == ' ' || c == '"' || c == '<' || c == '>' || c == '`'
}

func querySet(c byte) bool {
	return c0ControlSet(c) || c == ' ' || c == '"' || c == '#' || c == '<' || c == '>'
}

func specialQuerySet(c byte) bool { return querySet(c) || c == '\'' }

// pathSet is Node 22's (ada 2.9's) path percent-encode set, which does not include U+005E (^).
func pathSet(c byte) bool {
	return querySet(c) || c == '?' || c == '`' || c == '{' || c == '}'
}

func userinfoSet(c byte) bool {
	return pathSet(c) || c == '/' || c == ':' || c == ';' || c == '=' || c == '@' ||
		(c >= '[' && c <= '^') || c == '|'
}

func componentSet(c byte) bool {
	return userinfoSet(c) || (c >= '$' && c <= '&') || c == '+' || c == ','
}

func formURLEncodedSet(c byte) bool {
	return componentSet(c) || c == '!' || (c >= '\'' && c <= ')') || c == '~'
}

const upperHex = "0123456789ABCDEF"

func appendPercentEncoded(b *strings.Builder, s string, set encodeSet) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if set(c) {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		} else {
			b.WriteByte(c)
		}
	}
}

// percentEncodeString UTF-8 percent-encodes s, which must already be valid UTF-8.
func percentEncodeString(s string, set encodeSet) string {
	var b strings.Builder
	appendPercentEncoded(&b, s, set)
	return b.String()
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// percentDecode decodes %XX sequences into bytes; other bytes, and malformed sequences, are
// kept as they are. The result need not be valid UTF-8.
func percentDecode(s string) string {
	if strings.IndexByte(s, '%') < 0 {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				b = append(b, hi<<4|lo)
				i += 2
				continue
			}
		}
		b = append(b, s[i])
	}
	return string(b)
}
