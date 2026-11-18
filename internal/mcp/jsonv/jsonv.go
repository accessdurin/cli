// Package jsonv is a minimal, JavaScript-compatible JSON value model used by the MCP port.
//
// The TypeScript SDK parses every JSON-RPC message with JSON.parse and re-serializes it with
// JSON.stringify, so the bytes the CLI forwards or prints depend on JavaScript semantics:
// property order (array-index keys first, ascending, then string keys in insertion order),
// duplicate keys (the last value wins, at the first key's position), IEEE-754 numbers and the
// ECMAScript Number-to-String algorithm. This package reproduces those semantics for the subset
// the MCP port needs.
//
// INTEGRATION: internal/jsjson (written in parallel) is the module-wide JS-compatible JSON
// package. Once it lands, this package should become a thin alias over it, or be replaced by
// it; the exported surface here (Parse, Stringify, *Object, the Go types used for values) was
// kept deliberately small to make that swap mechanical.
//
// Values are represented with plain Go types:
//
//	null    -> nil
//	boolean -> bool
//	number  -> float64
//	string  -> string (lone UTF-16 surrogates are kept as WTF-8 byte sequences)
//	array   -> []any
//	object  -> *Object
package jsonv

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Object is a JSON object with JavaScript own-property ordering.
type Object struct {
	keys []string
	vals map[string]any
}

// NewObject builds an object from alternating key/value pairs.
func NewObject(kv ...any) *Object {
	o := &Object{vals: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

// arrayIndex reports whether key is a canonical array index ("0".."4294967294").
func arrayIndex(key string) (uint32, bool) {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil || n >= math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// Len returns the number of properties.
func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.keys)
}

// Keys returns the property names in JavaScript enumeration order.
func (o *Object) Keys() []string {
	if o == nil {
		return nil
	}
	return append([]string(nil), o.keys...)
}

// Has reports whether key is an own property.
func (o *Object) Has(key string) bool {
	if o == nil {
		return false
	}
	_, ok := o.vals[key]
	return ok
}

// Get returns the property value and whether it exists.
func (o *Object) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Set assigns a property. An existing property keeps its position, as in JavaScript.
func (o *Object) Set(key string, value any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; ok {
		o.vals[key] = value
		return
	}
	o.vals[key] = value
	if idx, ok := arrayIndex(key); ok {
		// Integer keys come first, in ascending order.
		pos := sort.Search(len(o.keys), func(i int) bool {
			k, isIdx := arrayIndex(o.keys[i])
			return !isIdx || k > idx
		})
		o.keys = append(o.keys, "")
		copy(o.keys[pos+1:], o.keys[pos:])
		o.keys[pos] = key
		return
	}
	o.keys = append(o.keys, key)
}

// Delete removes a property.
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

// Clone returns a shallow copy (the JavaScript `{...o}`).
func (o *Object) Clone() *Object {
	c := &Object{keys: append([]string(nil), o.keys...), vals: make(map[string]any, len(o.vals))}
	for k, v := range o.vals {
		c.vals[k] = v
	}
	return c
}

// MarshalJSON serializes the object with JSON.stringify semantics.
func (o *Object) MarshalJSON() ([]byte, error) { return Stringify(o) }

// UnmarshalJSON parses an object with JSON.parse semantics.
func (o *Object) UnmarshalJSON(data []byte) error {
	v, err := Parse(data)
	if err != nil {
		return err
	}
	obj, ok := v.(*Object)
	if !ok {
		return errors.New("jsonv: not a JSON object")
	}
	*o = *obj
	return nil
}

// DeepClone copies a value recursively.
func DeepClone(v any) any {
	switch t := v.(type) {
	case *Object:
		c := &Object{keys: append([]string(nil), t.keys...), vals: make(map[string]any, len(t.vals))}
		for k, x := range t.vals {
			c.vals[k] = DeepClone(x)
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, x := range t {
			c[i] = DeepClone(x)
		}
		return c
	default:
		return v
	}
}

// IsObject reports whether v is a JSON object (not null, not an array).
func IsObject(v any) bool {
	o, ok := v.(*Object)
	return ok && o != nil
}

// SyntaxError mirrors the JavaScript SyntaxError thrown by JSON.parse.
type SyntaxError struct{ Msg string }

func (e *SyntaxError) Error() string { return e.Msg }

// DecodeUTF8 decodes bytes the way Buffer#toString('utf8') and TextDecoder do: invalid
// sequences become U+FFFD.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size <= 1 {
			sb.WriteRune(utf8.RuneError)
			b = b[max(size, 1):]
			continue
		}
		sb.WriteRune(r)
		b = b[size:]
	}
	return sb.String()
}

// Parse implements JSON.parse over UTF-8 input (invalid UTF-8 is replaced with U+FFFD first).
func Parse(data []byte) (any, error) {
	return ParseString(DecodeUTF8(data))
}

// ParseString implements JSON.parse.
func ParseString(s string) (any, error) {
	p := &parser{s: s}
	p.ws()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.s) {
		return nil, p.errorf("Unexpected non-whitespace character after JSON at position %d", p.i)
	}
	return v, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) errorf(format string, args ...any) error {
	return &SyntaxError{Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) ws() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

const maxDepth = 10000

func (p *parser) value(depth int) (any, error) {
	if depth > maxDepth {
		return nil, p.errorf("Maximum nesting depth exceeded")
	}
	if p.i >= len(p.s) {
		return nil, p.errorf("Unexpected end of JSON input")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case c == 't':
		return p.lit("true", true)
	case c == 'f':
		return p.lit("false", false)
	case c == 'n':
		return p.lit("null", nil)
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return nil, p.errorf("Unexpected token '%c' at position %d", c, p.i)
	}
}

func (p *parser) lit(word string, v any) (any, error) {
	if strings.HasPrefix(p.s[p.i:], word) {
		p.i += len(word)
		return v, nil
	}
	return nil, p.errorf("Unexpected token at position %d", p.i)
}

func (p *parser) object(depth int) (any, error) {
	p.i++
	o := &Object{vals: map[string]any{}}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return o, nil
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.errorf("Expected property name at position %d", p.i)
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.errorf("Expected ':' after property name at position %d", p.i)
		}
		p.i++
		p.ws()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		o.Set(k.(string), v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.errorf("Unexpected end of JSON input")
		}
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.s[p.i] == '}' {
			p.i++
			return o, nil
		}
		return nil, p.errorf("Expected ',' or '}' after property value at position %d", p.i)
	}
}

func (p *parser) array(depth int) (any, error) {
	p.i++
	arr := []any{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return arr, nil
	}
	for {
		p.ws()
		v, err := p.value(depth + 1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		p.ws()
		if p.i >= len(p.s) {
			return nil, p.errorf("Unexpected end of JSON input")
		}
		if p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.s[p.i] == ']' {
			p.i++
			return arr, nil
		}
		return nil, p.errorf("Expected ',' or ']' after array element at position %d", p.i)
	}
}

func (p *parser) number() (any, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, p.errorf("No number after minus sign in JSON at position %d", p.i)
	}
	if p.s[p.i] == '0' {
		p.i++
	} else if p.s[p.i] >= '1' && p.s[p.i] <= '9' {
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	} else {
		return nil, p.errorf("No number after minus sign in JSON at position %d", p.i)
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if p.i >= len(p.s) || !isDigit(p.s[p.i]) {
			return nil, p.errorf("Unterminated fractional number in JSON at position %d", p.i)
		}
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if p.i >= len(p.s) || !isDigit(p.s[p.i]) {
			return nil, p.errorf("Exponent part is missing a number in JSON at position %d", p.i)
		}
		for p.i < len(p.s) && isDigit(p.s[p.i]) {
			p.i++
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.i], 64)
	if err != nil {
		var ne *strconv.NumError
		if !errors.As(err, &ne) || ne.Err != strconv.ErrRange {
			return nil, p.errorf("Invalid number in JSON at position %d", start)
		}
		// Out of range: JavaScript yields ±Infinity or ±0, which ParseFloat also returns.
	}
	return f, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// appendWTF8 appends a UTF-16 code unit sequence decoded from escapes, keeping lone surrogates.
func appendCodeUnit(sb *strings.Builder, r rune) {
	if r >= 0xD800 && r <= 0xDFFF {
		// WTF-8 encoding of a lone surrogate.
		sb.WriteByte(byte(0xE0 | (r >> 12)))
		sb.WriteByte(byte(0x80 | ((r >> 6) & 0x3F)))
		sb.WriteByte(byte(0x80 | (r & 0x3F)))
		return
	}
	sb.WriteRune(r)
}

func (p *parser) str() (any, error) {
	p.i++
	var sb strings.Builder
	start := p.i
	for {
		if p.i >= len(p.s) {
			return nil, p.errorf("Unterminated string in JSON at position %d", p.i)
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			sb.WriteString(p.s[start:p.i])
			p.i++
			return sb.String(), nil
		case c < 0x20:
			return nil, p.errorf("Bad control character in string literal in JSON at position %d", p.i)
		case c == '\\':
			sb.WriteString(p.s[start:p.i])
			p.i++
			if p.i >= len(p.s) {
				return nil, p.errorf("Unterminated string in JSON at position %d", p.i)
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case '"':
				sb.WriteByte('"')
			case '\\':
				sb.WriteByte('\\')
			case '/':
				sb.WriteByte('/')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				r, err := p.hex4()
				if err != nil {
					return nil, err
				}
				if utf16.IsSurrogate(r) && r < 0xDC00 && p.i+6 <= len(p.s) && p.s[p.i] == '\\' && p.s[p.i+1] == 'u' {
					save := p.i
					p.i += 2
					r2, err := p.hex4()
					if err == nil && r2 >= 0xDC00 && r2 <= 0xDFFF {
						sb.WriteRune(utf16.DecodeRune(r, r2))
						break
					}
					p.i = save
				}
				appendCodeUnit(&sb, r)
			default:
				return nil, p.errorf("Bad escaped character in JSON at position %d", p.i-1)
			}
			start = p.i
		default:
			p.i++
		}
	}
}

func (p *parser) hex4() (rune, error) {
	if p.i+4 > len(p.s) {
		return 0, p.errorf("Bad Unicode escape in JSON at position %d", p.i)
	}
	var r rune
	for k := 0; k < 4; k++ {
		h := hexVal(p.s[p.i+k])
		if h < 0 {
			return 0, p.errorf("Bad Unicode escape in JSON at position %d", p.i)
		}
		r = r<<4 | rune(h)
	}
	p.i += 4
	return r, nil
}

// Stringify implements JSON.stringify for jsonv values (no indentation). Non-finite numbers
// serialize as null, as in JavaScript.
func Stringify(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := write(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MustStringify is Stringify for values known to be serializable.
func MustStringify(v any) string {
	b, err := Stringify(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func write(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case float64:
		buf.WriteString(FormatNumber(t))
	case int:
		buf.WriteString(FormatNumber(float64(t)))
	case int64:
		buf.WriteString(FormatNumber(float64(t)))
	case string:
		writeString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := write(buf, x); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case []string:
		buf.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, x)
		}
		buf.WriteByte(']')
	case *Object:
		if t == nil {
			buf.WriteString("null")
			return nil
		}
		buf.WriteByte('{')
		first := true
		for _, k := range t.keys {
			x := t.vals[k]
			if _, undef := x.(undefinedType); undef {
				continue
			}
			if !first {
				buf.WriteByte(',')
			}
			first = false
			writeString(buf, k)
			buf.WriteByte(':')
			if err := write(buf, x); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case undefinedType:
		buf.WriteString("null")
	default:
		return fmt.Errorf("jsonv: cannot stringify %T", v)
	}
	return nil
}

type undefinedType struct{}

// Undefined is a property value that JSON.stringify omits. It lets ported code keep a key's
// position (as JavaScript does for a property assigned `undefined`) without serializing it.
var Undefined any = undefinedType{}

// IsUndefined reports whether v is Undefined.
func IsUndefined(v any) bool {
	_, ok := v.(undefinedType)
	return ok
}

const hexDigits = "0123456789abcdef"

func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				buf.WriteString(`\"`)
			case '\\':
				buf.WriteString(`\\`)
			case '\b':
				buf.WriteString(`\b`)
			case '\f':
				buf.WriteString(`\f`)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				if c < 0x20 {
					buf.WriteString(`\u00`)
					buf.WriteByte(hexDigits[c>>4])
					buf.WriteByte(hexDigits[c&0xF])
				} else {
					buf.WriteByte(c)
				}
			}
			i++
			continue
		}
		// WTF-8 lone surrogate (ED A0..BF xx) is written as a \u escape, as well-formed
		// JSON.stringify does.
		if c == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2] >= 0x80 && s[i+2] <= 0xBF {
			r := rune(c&0x0F)<<12 | rune(s[i+1]&0x3F)<<6 | rune(s[i+2]&0x3F)
			buf.WriteString(`\u`)
			buf.WriteByte(hexDigits[(r>>12)&0xF])
			buf.WriteByte(hexDigits[(r>>8)&0xF])
			buf.WriteByte(hexDigits[(r>>4)&0xF])
			buf.WriteByte(hexDigits[r&0xF])
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			buf.WriteString("�")
		} else {
			buf.WriteString(s[i : i+size])
		}
		i += size
	}
	buf.WriteByte('"')
}

// FormatNumber implements ECMAScript Number::toString(10).
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f) || math.IsInf(f, 0):
		return "null"
	case f == 0:
		return "0"
	}
	neg := f < 0
	if neg {
		f = -f
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.dddde±XX
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k := len(digits)
	n := exp + 1
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		sign := "+"
		if n-1 < 0 {
			sign = "-"
		}
		ev := n - 1
		if ev < 0 {
			ev = -ev
		}
		if k == 1 {
			out = digits + "e" + sign + strconv.Itoa(ev)
		} else {
			out = digits[:1] + "." + digits[1:] + "e" + sign + strconv.Itoa(ev)
		}
	}
	if neg {
		return "-" + out
	}
	return out
}

// TypeName returns the JavaScript-ish type name used in error messages.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case *Object:
		return "object"
	case undefinedType:
		return "undefined"
	}
	return fmt.Sprintf("%T", v)
}

// Path walks object properties; it returns (nil, false) when any step is missing or not an
// object.
func Path(v any, keys ...string) (any, bool) {
	cur := v
	for _, k := range keys {
		o, ok := cur.(*Object)
		if !ok || o == nil {
			return nil, false
		}
		cur, ok = o.Get(k)
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// Equal reports deep equality, comparing objects in property order as JSON.stringify would.
func Equal(a, b any) bool {
	sa, err1 := Stringify(a)
	sb, err2 := Stringify(b)
	return err1 == nil && err2 == nil && bytes.Equal(sa, sb)
}
