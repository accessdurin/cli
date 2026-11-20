// Package jsonc is a Go port of the parts of the npm package jsonc-parser
// 3.3.1 that the durin CLI uses: the scanner, Parse/ParseTree with error
// recovery, FindNodeAtLocation, Format, Modify/Remove (setProperty) and
// ApplyEdits. The algorithms follow lib/esm/impl/*.js closely so that editing
// a file produces byte-identical output to the TypeScript CLI. Upstream is
// Copyright (c) Microsoft under the MIT license; its notice is kept in
// LICENSE-jsonc-parser.md next to this file.
//
// # Offsets
//
// Upstream works on UTF-16 strings; this port works on Go strings (UTF-8
// bytes). Every offset and length in this package (ParseError, Node, Edit,
// Range) is a byte offset into the UTF-8 text. Because every token boundary
// the algorithms act on is an ASCII character, the edited text is identical.
//
// # Values
//
// JSON values are represented as plain Go values:
//
//	null    nil
//	boolean bool
//	number  float64 (Stringify also accepts Go integer types and float32)
//	string  string
//	array   []any (Stringify also accepts []string)
//	object  *Object (Stringify also accepts Object)
//
// Object keeps keys in JavaScript own-property order: array-index keys
// ("0" … "4294967294", canonical form) first in ascending numeric order, then
// all other keys in insertion order. This is the order JSON.stringify and
// Object.keys use in JavaScript, so values built here serialize exactly like
// the equivalent JavaScript object literal.
//
// Strings decoded from \uXXXX escapes that form an unpaired UTF-16 surrogate
// are stored as WTF-8 (the 3-byte generalized UTF-8 encoding of the
// surrogate), and Stringify writes such sequences back as \udxxx, as
// JavaScript's JSON.stringify does.
package jsonc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Object is an ordered JSON object. The zero value is an empty object ready
// to use. Key order follows JavaScript own-property order (see the package
// documentation).
type Object struct {
	keys []string
	vals map[string]any
	// protoDetached records that the object's prototype chain no longer
	// reaches Object.prototype, which only Parse uses to emulate how
	// JavaScript treats a "__proto__" key (see Parse).
	protoDetached bool
}

// NewObject returns an empty Object.
func NewObject() *Object { return &Object{} }

// Set sets key to v, keeping an existing key in its position, and returns o
// so calls can be chained.
func (o *Object) Set(key string, v any) *Object {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; ok {
		o.vals[key] = v
		return o
	}
	o.vals[key] = v
	idx, isIndex := arrayIndex(key)
	if !isIndex {
		o.keys = append(o.keys, key)
		return o
	}
	pos := 0
	for pos < len(o.keys) {
		other, ok := arrayIndex(o.keys[pos])
		if !ok || other > idx {
			break
		}
		pos++
	}
	o.keys = append(o.keys, "")
	copy(o.keys[pos+1:], o.keys[pos:])
	o.keys[pos] = key
	return o
}

// Get returns the value stored under key.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Delete removes key if present.
func (o *Object) Delete(key string) {
	if o == nil {
		return
	}
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Keys returns a copy of the keys in order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Len returns the number of keys.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// arrayIndex reports whether key is a JavaScript array index: the canonical
// decimal form of an integer in [0, 2^32-2].
func arrayIndex(key string) (uint32, bool) {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	var n uint64
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	if n > math.MaxUint32-1 {
		return 0, false
	}
	return uint32(n), true
}

// Stringify serializes v like JavaScript's JSON.stringify(v) with no
// indentation. It returns an error for Go types that have no JSON
// representation in this package.
func Stringify(v any) (string, error) {
	var b strings.Builder
	if err := stringify(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func stringify(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		quote(b, x)
	case float64:
		b.WriteString(formatNumber(x))
	case float32:
		b.WriteString(formatNumber(float64(x)))
	case int:
		b.WriteString(formatNumber(float64(x)))
	case int8:
		b.WriteString(formatNumber(float64(x)))
	case int16:
		b.WriteString(formatNumber(float64(x)))
	case int32:
		b.WriteString(formatNumber(float64(x)))
	case int64:
		b.WriteString(formatNumber(float64(x)))
	case uint:
		b.WriteString(formatNumber(float64(x)))
	case uint8:
		b.WriteString(formatNumber(float64(x)))
	case uint16:
		b.WriteString(formatNumber(float64(x)))
	case uint32:
		b.WriteString(formatNumber(float64(x)))
	case uint64:
		b.WriteString(formatNumber(float64(x)))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := stringify(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []string:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			quote(b, e)
		}
		b.WriteByte(']')
	case *Object:
		if x == nil {
			b.WriteString("null")
			return nil
		}
		return stringifyObject(b, x)
	case Object:
		return stringifyObject(b, &x)
	default:
		return fmt.Errorf("jsonc: cannot stringify value of type %T", v)
	}
	return nil
}

func stringifyObject(b *strings.Builder, o *Object) error {
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		quote(b, k)
		b.WriteByte(':')
		if err := stringify(b, o.vals[k]); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// formatNumber implements JavaScript's Number::toString for JSON: NaN and
// ±Infinity become null, -0 becomes 0.
func formatNumber(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign = "-"
		f = -f
	}
	// Shortest round-tripping digits, as ECMAScript requires.
	s := strconv.FormatFloat(f, 'e', -1, 64)
	e := strings.IndexByte(s, 'e')
	digits := strings.Replace(s[:e], ".", "", 1)
	exp, _ := strconv.Atoi(s[e+1:])
	k, n := len(digits), exp+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

const hexDigits = "0123456789abcdef"

// quote writes s as a JSON string literal the way JSON.stringify does:
// only '"', '\\', C0 controls and unpaired surrogates are escaped.
// Invalid UTF-8 bytes (which a JavaScript string cannot hold) are written
// as U+FFFD.
func quote(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					b.WriteString(`\u00`)
					b.WriteByte(hexDigits[c>>4])
					b.WriteByte(hexDigits[c&0xF])
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		if u, ok := surrogateAt(s, i); ok {
			if u < 0xDC00 {
				if lo, ok := surrogateAt(s, i+3); ok && lo >= 0xDC00 {
					b.WriteRune(rune(0x10000 + (u-0xD800)<<10 + (lo - 0xDC00)))
					i += 6
					continue
				}
			}
			b.WriteString(`\u`)
			for shift := 12; shift >= 0; shift -= 4 {
				b.WriteByte(hexDigits[(u>>shift)&0xF])
			}
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

// surrogateAt decodes a WTF-8 encoded surrogate code point at s[i:].
func surrogateAt(s string, i int) (int, bool) {
	if i+2 >= len(s) || s[i] != 0xED || s[i+1] < 0xA0 || s[i+1] > 0xBF || s[i+2]&0xC0 != 0x80 {
		return 0, false
	}
	return 0xD000 | int(s[i+1]&0x3F)<<6 | int(s[i+2]&0x3F), true
}

// appendCodeUnit appends a BMP code point, encoding surrogates as WTF-8.
func appendCodeUnit(b *strings.Builder, u int) {
	if u >= 0xD800 && u <= 0xDFFF {
		b.WriteByte(byte(0xE0 | u>>12))
		b.WriteByte(byte(0x80 | (u>>6)&0x3F))
		b.WriteByte(byte(0x80 | u&0x3F))
		return
	}
	b.WriteRune(rune(u))
}
