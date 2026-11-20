package jsjson

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Stringify is JSON.stringify(v). For a top-level nil (undefined) or a value
// whose ToJSON returns nil, JavaScript returns undefined; Stringify returns "".
func Stringify(v Value) string {
	return StringifyIndent(v, 0)
}

// StringifyIndent is JSON.stringify(v, null, indent): indent is clamped to
// 0..10 spaces, and 0 means no whitespace at all.
func StringifyIndent(v Value, indent int) string {
	if indent > 10 {
		indent = 10
	}
	gap := ""
	if indent > 0 {
		gap = strings.Repeat(" ", indent)
	}
	var b strings.Builder
	w := writer{b: &b, gap: gap}
	w.value(v, "")
	return b.String()
}

type writer struct {
	b   *strings.Builder
	gap string
}

func resolve(v Value) Value {
	if j, ok := v.(JSONer); ok {
		return j.ToJSON()
	}
	return v
}

// value writes v and reports whether anything was written (false for undefined).
func (w *writer) value(v Value, indent string) bool {
	switch x := resolve(v).(type) {
	case nil:
		return false
	case Null:
		w.b.WriteString("null")
	case Bool:
		if x {
			w.b.WriteString("true")
		} else {
			w.b.WriteString("false")
		}
	case Number:
		if math.IsInf(float64(x), 0) || math.IsNaN(float64(x)) {
			w.b.WriteString("null")
		} else {
			w.b.WriteString(FormatNumber(float64(x)))
		}
	case String:
		w.b.WriteString(Quote(string(x)))
	case Array:
		if len(x) == 0 {
			w.b.WriteString("[]")
			return true
		}
		inner := indent + w.gap
		w.b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				w.b.WriteByte(',')
			}
			if w.gap != "" {
				w.b.WriteByte('\n')
				w.b.WriteString(inner)
			}
			if !w.value(e, inner) {
				w.b.WriteString("null")
			}
		}
		if w.gap != "" {
			w.b.WriteByte('\n')
			w.b.WriteString(indent)
		}
		w.b.WriteByte(']')
	case *Object:
		inner := indent + w.gap
		w.b.WriteByte('{')
		wrote := false
		for _, k := range x.Keys() {
			e := resolve(x.vals[k])
			if e == nil {
				continue
			}
			if wrote {
				w.b.WriteByte(',')
			}
			if w.gap != "" {
				w.b.WriteByte('\n')
				w.b.WriteString(inner)
			}
			wrote = true
			w.b.WriteString(Quote(k))
			w.b.WriteByte(':')
			if w.gap != "" {
				w.b.WriteByte(' ')
			}
			w.value(e, inner)
		}
		if wrote && w.gap != "" {
			w.b.WriteByte('\n')
			w.b.WriteString(indent)
		}
		w.b.WriteByte('}')
	default:
		// An Extension without ToJSON is an object without own keys.
		w.b.WriteString("{}")
	}
	return true
}

const hexDigits = "0123456789abcdef"

// Quote is JSON.stringify(s) for a string: it escapes '"', '\\', \b \f \n \r
// \t, other C0 controls as \u00xx, and lone surrogates as \udxxx (lowercase
// hex), and writes everything else raw.
func Quote(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			i++
			switch c {
			case '"':
				b = append(b, '\\', '"')
			case '\\':
				b = append(b, '\\', '\\')
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				if c < 0x20 {
					b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
				} else {
					b = append(b, c)
				}
			}
			continue
		}
		r, n := decodeRune(s, i, true)
		i += n
		if r >= 0xD800 && r <= 0xDBFF && i < len(s) {
			// A pair split into two three-byte encodings is still a pair.
			if r2, n2 := decodeRune(s, i, true); r2 >= 0xDC00 && r2 <= 0xDFFF {
				i += n2
				b = utf8.AppendRune(b, 0x10000+(r-0xD800)<<10+(r2-0xDC00))
				continue
			}
		}
		if isSurrogate(r) {
			b = append(b, '\\', 'u', 'd', hexDigits[(r>>8)&0xF], hexDigits[(r>>4)&0xF], hexDigits[r&0xF])
			continue
		}
		b = utf8.AppendRune(b, r)
	}
	b = append(b, '"')
	return string(b)
}

// FormatNumber is JavaScript's Number::toString(x) (String(x)): the shortest
// digits that round-trip, in fixed notation for decimal exponents from -7 to
// 21 (exclusive) and as d.ddde±n otherwise. -0 gives "0".
func FormatNumber(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case x == 0:
		return "0"
	}
	sign := ""
	if x < 0 {
		sign, x = "-", -x
	}
	e := strconv.FormatFloat(x, 'e', -1, 64) // d.ddddde±XX
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	k := len(digits)
	ex, _ := strconv.Atoi(exp)
	n := ex + 1
	var out string
	switch {
	case k <= n && n <= 21:
		out = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		out = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		out = "0." + strings.Repeat("0", -n) + digits
	default:
		es := "+"
		if n-1 < 0 {
			es = "-"
		}
		ev := n - 1
		if ev < 0 {
			ev = -ev
		}
		if k == 1 {
			out = digits + "e" + es + strconv.Itoa(ev)
		} else {
			out = digits[:1] + "." + digits[1:] + "e" + es + strconv.Itoa(ev)
		}
	}
	return sign + out
}

// TerminalJSON ports terminalJson from src/terminal-text.ts:
// JSON.stringify(v, null, 2) with DEL, C1 controls and bidi controls
// (U+007F-U+009F, U+061C, U+200E, U+200F, U+202A-U+202E, U+2066-U+2069)
// additionally escaped as \u plus four lowercase hex digits.
func TerminalJSON(v Value) string {
	s := StringifyIndent(v, 2)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 0x7F && r <= 0x9F) || r == 0x061C || r == 0x200E || r == 0x200F ||
			(r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) {
			b.WriteString(`\u`)
			b.WriteByte(hexDigits[(r>>12)&0xF])
			b.WriteByte(hexDigits[(r>>8)&0xF])
			b.WriteByte(hexDigits[(r>>4)&0xF])
			b.WriteByte(hexDigits[r&0xF])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CanonicalJSON ports canonicalJson from src/canonical-json.ts: object keys
// are sorted recursively by UTF-16 code units (Array#toSorted), arrays keep
// their order, and the result is JSON.stringify'd. Because the rebuilt
// objects are JavaScript objects, array-index keys still come first in
// numeric order. Extension values (such as dates) become {}.
func CanonicalJSON(v Value) string {
	return Stringify(Canonicalize(v))
}

// Canonicalize returns the normalized value that CanonicalJSON stringifies.
func Canonicalize(v Value) Value {
	switch x := v.(type) {
	case Array:
		out := make(Array, len(x))
		for i, e := range x {
			out[i] = Canonicalize(e)
		}
		return out
	case *Object:
		keys := x.Keys()
		sort.SliceStable(keys, func(i, j int) bool { return CompareUTF16(keys[i], keys[j]) < 0 })
		o := &Object{}
		for _, k := range keys {
			o.Set(k, Canonicalize(x.vals[k]))
		}
		return o
	case nil, Null, Bool, Number, String:
		return v
	}
	return &Object{}
}
