package jsjson

import (
	"unicode/utf16"
	"unicode/utf8"
)

// String representation
//
// JavaScript strings are sequences of UTF-16 code units and may contain lone
// surrogates (for example the result of JSON.parse(`"\ud800"`)). Go strings in
// this package hold WTF-8 (https://simonsapin.github.io/wtf-8/): ordinary
// UTF-8, except that a lone surrogate code unit U+D800..U+DFFF is stored as its
// three-byte generalized UTF-8 encoding (ED A0 80 .. ED BF BF). A surrogate pair
// is always stored as the four-byte UTF-8 encoding of its code point, never as
// two three-byte sequences. Well-formed text is therefore plain UTF-8, and
// every JavaScript string maps to exactly one Go string and back.
//
// Parse, ParseString, DecodeUTF8 and FromUTF16 always produce this canonical
// form. Bytes in a Go string that are not WTF-8 at all (stray continuation
// bytes, truncated sequences, ...) are read as U+FFFD, one per maximal invalid
// subpart, exactly as Node's UTF-8 decoder would read them.

// decodeRune decodes one code point from s at i. With wtf8 set, three-byte
// encodings of surrogates are accepted and returned as surrogate code points.
// Invalid input yields U+FFFD with the length of the maximal invalid subpart
// (WHATWG Encoding Standard, UTF-8 decoder).
func decodeRune(s string, i int, wtf8 bool) (rune, int) {
	r, n := decodeRaw(s, i, wtf8)
	if r < 0 {
		return utf8.RuneError, n
	}
	return r, n
}

// decodeRaw is decodeRune but reports invalid input as -1.
func decodeRaw(s string, i int, wtf8 bool) (rune, int) {
	b0 := s[i]
	if b0 < 0x80 {
		return rune(b0), 1
	}
	var need int
	var lo, hi byte = 0x80, 0xBF
	var cp rune
	switch {
	case b0 >= 0xC2 && b0 <= 0xDF:
		need, cp = 1, rune(b0&0x1F)
	case b0 >= 0xE0 && b0 <= 0xEF:
		need, cp = 2, rune(b0&0x0F)
		if b0 == 0xE0 {
			lo = 0xA0
		} else if b0 == 0xED && !wtf8 {
			hi = 0x9F
		}
	case b0 >= 0xF0 && b0 <= 0xF4:
		need, cp = 3, rune(b0&0x07)
		if b0 == 0xF0 {
			lo = 0x90
		} else if b0 == 0xF4 {
			hi = 0x8F
		}
	default:
		return -1, 1
	}
	n := 1
	for k := 0; k < need; k++ {
		if i+n >= len(s) {
			return -1, n
		}
		b := s[i+n]
		if b < lo || b > hi {
			return -1, n
		}
		lo, hi = 0x80, 0xBF
		cp = cp<<6 | rune(b&0x3F)
		n++
	}
	return cp, n
}

// DecodeUTF8 decodes bytes the way Node's Buffer#toString('utf8') does: the
// WHATWG UTF-8 decoder with replacement, one U+FFFD per maximal invalid
// subpart, and a leading byte order mark kept as U+FEFF. The result never
// contains surrogates.
func DecodeUTF8(b []byte) string {
	s := string(b)
	if utf8.ValidString(s) {
		return s
	}
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); {
		r, n := decodeRaw(s, i, false)
		if r < 0 {
			out = append(out, "\uFFFD"...)
		} else {
			out = append(out, s[i:i+n]...)
		}
		i += n
	}
	return string(out)
}

// DecodeUTF8StripBOM decodes like TextDecoder('utf-8') and fetch's
// Response#text()/json(): as DecodeUTF8, but a leading byte order mark is
// removed.
func DecodeUTF8StripBOM(b []byte) string {
	s := DecodeUTF8(b)
	if len(s) >= 3 && s[:3] == "\uFEFF" {
		return s[3:]
	}
	return s
}

func isSurrogate(r rune) bool { return r >= 0xD800 && r <= 0xDFFF }

// appendCodePoint appends one UTF-16 code unit (or code point) to a WTF-8 buffer,
// joining a low surrogate with a directly preceding lone high surrogate.
func appendCodePoint(b []byte, r rune) []byte {
	if r >= 0xDC00 && r <= 0xDFFF && len(b) >= 3 {
		t := b[len(b)-3:]
		if t[0] == 0xED && t[1] >= 0xA0 && t[1] <= 0xAF {
			high := rune(0xD000) | rune(t[1]&0x3F)<<6 | rune(t[2]&0x3F)
			return utf8.AppendRune(b[:len(b)-3], utf16.DecodeRune(high, r))
		}
	}
	if isSurrogate(r) {
		return append(b, 0xED, byte(0x80|(r>>6)&0x3F), byte(0x80|r&0x3F))
	}
	return utf8.AppendRune(b, r)
}

// FromUTF16 converts UTF-16 code units (a JavaScript string) to the WTF-8
// representation used by this package.
func FromUTF16(u []uint16) string {
	b := make([]byte, 0, len(u))
	for _, c := range u {
		b = appendCodePoint(b, rune(c))
	}
	return string(b)
}

// ToUTF16 converts a string to UTF-16 code units, as JavaScript stores it.
func ToUTF16(s string) []uint16 {
	u := make([]uint16, 0, len(s))
	for i := 0; i < len(s); {
		r, n := decodeRune(s, i, true)
		i += n
		if r >= 0x10000 {
			r1, r2 := utf16.EncodeRune(r)
			u = append(u, uint16(r1), uint16(r2))
		} else {
			u = append(u, uint16(r))
		}
	}
	return u
}

// UTF16Len returns the JavaScript length (number of UTF-16 code units) of s.
func UTF16Len(s string) int {
	n := 0
	for i := 0; i < len(s); {
		r, w := decodeRune(s, i, true)
		i += w
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// IsWellFormed reports whether s has no lone surrogates, like
// String.prototype.isWellFormed. Separately encoded surrogate halves that
// form a pair count as well formed, as they would in JavaScript.
func IsWellFormed(s string) bool {
	u := ToUTF16(s)
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
			i++
		} else if c >= 0xD800 && c <= 0xDFFF {
			return false
		}
	}
	return true
}

// ToWellFormed replaces lone surrogates with U+FFFD, like
// String.prototype.toWellFormed.
func ToWellFormed(s string) string {
	if IsWellFormed(s) {
		return FromUTF16(ToUTF16(s))
	}
	u := ToUTF16(s)
	for i := 0; i < len(u); i++ {
		c := u[i]
		if c >= 0xD800 && c <= 0xDBFF && i+1 < len(u) && u[i+1] >= 0xDC00 && u[i+1] <= 0xDFFF {
			i++
		} else if c >= 0xD800 && c <= 0xDFFF {
			u[i] = 0xFFFD
		}
	}
	return FromUTF16(u)
}

// CompareUTF16 compares strings by UTF-16 code units, the order used by
// JavaScript's default Array#sort and the < operator on strings. It returns
// -1, 0 or +1.
func CompareUTF16(a, b string) int {
	ua, ub := ToUTF16(a), ToUTF16(b)
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}
