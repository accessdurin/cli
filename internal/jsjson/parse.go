package jsjson

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// SyntaxError is returned when the input is not something JSON.parse
// accepts. Messages are not V8's; only acceptance is meant to match.
type SyntaxError struct {
	Offset int // byte offset into the (decoded) input
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("invalid JSON at byte %d: %s", e.Offset, e.Msg)
}

// Parse is JSON.parse(buffer.toString('utf8')): it decodes b as Node's
// Buffer#toString does (invalid UTF-8 becomes U+FFFD, a byte order mark is
// kept and therefore rejected) and parses the result.
func Parse(b []byte) (Value, error) {
	return ParseString(DecodeUTF8(b))
}

// ParseString is JSON.parse for a string in this package's WTF-8 form.
func ParseString(s string) (Value, error) {
	p := parser{s: s}
	p.space()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.i < len(p.s) {
		return nil, p.fail("unexpected non-whitespace after JSON value")
	}
	return v, nil
}

type parser struct {
	s string
	i int
}

func (p *parser) fail(msg string) error {
	if p.i >= len(p.s) {
		msg = "unexpected end of input: " + msg
	}
	return &SyntaxError{Offset: p.i, Msg: msg}
}

func (p *parser) space() {
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) literal(word string, v Value) (Value, error) {
	if len(p.s)-p.i >= len(word) && p.s[p.i:p.i+len(word)] == word {
		p.i += len(word)
		return v, nil
	}
	return nil, p.fail("unexpected token")
}

func (p *parser) value() (Value, error) {
	if p.i >= len(p.s) {
		return nil, p.fail("expected a value")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return String(s), nil
	case c == 't':
		return p.literal("true", Bool(true))
	case c == 'f':
		return p.literal("false", Bool(false))
	case c == 'n':
		return p.literal("null", Null{})
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	return nil, p.fail("unexpected token")
}

func (p *parser) object() (Value, error) {
	p.i++ // {
	o := &Object{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return o, nil
	}
	for {
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return nil, p.fail("expected a property name")
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, p.fail("expected ':'")
		}
		p.i++
		p.space()
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		o.Set(k, v)
		p.space()
		if p.i >= len(p.s) {
			return nil, p.fail("expected ',' or '}'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			p.space()
		case '}':
			p.i++
			return o, nil
		default:
			return nil, p.fail("expected ',' or '}'")
		}
	}
}

func (p *parser) array() (Value, error) {
	p.i++ // [
	a := Array{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return a, nil
	}
	for {
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.space()
		if p.i >= len(p.s) {
			return nil, p.fail("expected ',' or ']'")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
			p.space()
		case ']':
			p.i++
			return a, nil
		default:
			return nil, p.fail("expected ',' or ']'")
		}
	}
}

func hexVal(c byte) rune {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0')
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10
	}
	return -1
}

// str parses a string literal starting at the opening quote.
func (p *parser) str() (string, error) {
	p.i++ // "
	start := p.i
	// Fast path: plain UTF-8 without escapes or surrogate encodings.
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' {
			seg := p.s[start:p.i]
			if utf8.ValidString(seg) {
				p.i++
				return seg, nil
			}
			break
		}
		if c == '\\' || c < 0x20 || c == 0xED {
			break
		}
		p.i++
	}
	p.i = start
	var b []byte
	for {
		if p.i >= len(p.s) {
			return "", p.fail("unterminated string")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return string(b), nil
		case c < 0x20:
			return "", p.fail("bad control character in string literal")
		case c == '\\':
			p.i++
			if p.i >= len(p.s) {
				return "", p.fail("bad escape")
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case '"', '\\', '/':
				b = append(b, e)
			case 'b':
				b = append(b, '\b')
			case 'f':
				b = append(b, '\f')
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			case 'u':
				if len(p.s)-p.i < 4 {
					return "", p.fail("bad unicode escape")
				}
				var r rune
				for k := 0; k < 4; k++ {
					h := hexVal(p.s[p.i+k])
					if h < 0 {
						return "", p.fail("bad unicode escape")
					}
					r = r<<4 | h
				}
				p.i += 4
				b = appendCodePoint(b, r)
			default:
				p.i--
				return "", p.fail("bad escaped character")
			}
		case c < 0x80:
			b = append(b, c)
			p.i++
		default:
			r, n := decodeRune(p.s, p.i, true)
			b = appendCodePoint(b, r)
			p.i += n
		}
	}
}

func (p *parser) digits() int {
	n := 0
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
		n++
	}
	return n
}

func (p *parser) number() (Value, error) {
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	if p.i >= len(p.s) {
		return nil, p.fail("no number after minus sign")
	}
	if p.s[p.i] == '0' {
		p.i++
	} else if p.digits() == 0 {
		return nil, p.fail("no number after minus sign")
	}
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if p.digits() == 0 {
			return nil, p.fail("unterminated fractional number")
		}
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			p.i++
		}
		if p.digits() == 0 {
			return nil, p.fail("exponent part is missing a number")
		}
	}
	return Number(parseFloat(p.s[start:p.i])), nil
}

// parseFloat converts a valid decimal literal with JavaScript's rounding:
// out-of-range magnitudes become ±Infinity or ±0.
func parseFloat(text string) float64 {
	f, _ := strconv.ParseFloat(text, 64) // ErrRange still yields ±Inf / ±0
	return f
}
