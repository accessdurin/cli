package jsonc

import "strings"

// Port of lib/esm/impl/scanner.js. Positions are byte offsets; every byte
// the scanner tests for is ASCII, so tokens span the same characters as
// upstream. Line/column tracking is not ported (nothing here uses it).

type syntaxKind int

const (
	tokOpenBrace    syntaxKind = 1
	tokCloseBrace   syntaxKind = 2
	tokOpenBracket  syntaxKind = 3
	tokCloseBracket syntaxKind = 4
	tokComma        syntaxKind = 5
	tokColon        syntaxKind = 6
	tokNull         syntaxKind = 7
	tokTrue         syntaxKind = 8
	tokFalse        syntaxKind = 9
	tokString       syntaxKind = 10
	tokNumber       syntaxKind = 11
	tokLineComment  syntaxKind = 12
	tokBlockComment syntaxKind = 13
	tokLineBreak    syntaxKind = 14
	tokTrivia       syntaxKind = 15
	tokUnknown      syntaxKind = 16
	tokEOF          syntaxKind = 17
)

type scanError int

const (
	scanErrNone                   scanError = 0
	scanErrUnexpectedEndOfComment scanError = 1
	scanErrUnexpectedEndOfString  scanError = 2
	scanErrUnexpectedEndOfNumber  scanError = 3
	scanErrInvalidUnicode         scanError = 4
	scanErrInvalidEscapeCharacter scanError = 5
	scanErrInvalidCharacter       scanError = 6
)

type scanner struct {
	text        string
	pos         int
	value       string
	tokenOffset int
	token       syntaxKind
	err         scanError
}

func newScanner(text string) *scanner {
	return &scanner{text: text, token: tokUnknown}
}

// at mirrors String.prototype.charCodeAt: -1 stands in for NaN.
func (s *scanner) at(i int) int {
	if i < 0 || i >= len(s.text) {
		return -1
	}
	return int(s.text[i])
}

func (s *scanner) tokenLength() int { return s.pos - s.tokenOffset }

func (s *scanner) scanHexDigits(count int) int {
	digits, value := 0, 0
	for digits < count {
		ch := s.at(s.pos)
		switch {
		case ch >= '0' && ch <= '9':
			value = value*16 + ch - '0'
		case ch >= 'A' && ch <= 'F':
			value = value*16 + ch - 'A' + 10
		case ch >= 'a' && ch <= 'f':
			value = value*16 + ch - 'a' + 10
		default:
			return -1
		}
		s.pos++
		digits++
	}
	return value
}

func (s *scanner) scanNumber() string {
	start := s.pos
	if s.at(s.pos) == '0' {
		s.pos++
	} else {
		s.pos++
		for s.pos < len(s.text) && isDigit(s.at(s.pos)) {
			s.pos++
		}
	}
	if s.pos < len(s.text) && s.at(s.pos) == '.' {
		s.pos++
		if s.pos < len(s.text) && isDigit(s.at(s.pos)) {
			s.pos++
			for s.pos < len(s.text) && isDigit(s.at(s.pos)) {
				s.pos++
			}
		} else {
			s.err = scanErrUnexpectedEndOfNumber
			return s.text[start:s.pos]
		}
	}
	end := s.pos
	if s.pos < len(s.text) && (s.at(s.pos) == 'E' || s.at(s.pos) == 'e') {
		s.pos++
		if s.at(s.pos) == '+' || s.at(s.pos) == '-' {
			s.pos++
		}
		if s.pos < len(s.text) && isDigit(s.at(s.pos)) {
			s.pos++
			for s.pos < len(s.text) && isDigit(s.at(s.pos)) {
				s.pos++
			}
			end = s.pos
		} else {
			s.err = scanErrUnexpectedEndOfNumber
		}
	}
	return s.text[start:end]
}

// stringBuilder accumulates a decoded string literal, joining \uXXXX
// surrogate escapes that are adjacent in the result the way concatenating
// UTF-16 code units does in JavaScript.
type stringBuilder struct {
	b       strings.Builder
	pending int // unpaired high surrogate waiting for a low one, or -1
}

func (sb *stringBuilder) flush() {
	if sb.pending >= 0 {
		appendCodeUnit(&sb.b, sb.pending)
		sb.pending = -1
	}
}

func (sb *stringBuilder) raw(s string) {
	if s == "" {
		return
	}
	sb.flush()
	sb.b.WriteString(s)
}

func (sb *stringBuilder) unit(u int) {
	if u >= 0xDC00 && u <= 0xDFFF && sb.pending >= 0 {
		sb.b.WriteRune(rune(0x10000 + (sb.pending-0xD800)<<10 + (u - 0xDC00)))
		sb.pending = -1
		return
	}
	sb.flush()
	if u >= 0xD800 && u <= 0xDBFF {
		sb.pending = u
		return
	}
	appendCodeUnit(&sb.b, u)
}

func (sb *stringBuilder) String() string {
	sb.flush()
	return sb.b.String()
}

func (s *scanner) scanString() string {
	result := stringBuilder{pending: -1}
	start := s.pos
	n := len(s.text)
	for {
		if s.pos >= n {
			result.raw(s.text[start:s.pos])
			s.err = scanErrUnexpectedEndOfString
			break
		}
		ch := s.at(s.pos)
		if ch == '"' {
			result.raw(s.text[start:s.pos])
			s.pos++
			break
		}
		if ch == '\\' {
			result.raw(s.text[start:s.pos])
			s.pos++
			if s.pos >= n {
				s.err = scanErrUnexpectedEndOfString
				break
			}
			ch2 := s.at(s.pos)
			s.pos++
			switch ch2 {
			case '"':
				result.raw("\"")
			case '\\':
				result.raw("\\")
			case '/':
				result.raw("/")
			case 'b':
				result.raw("\b")
			case 'f':
				result.raw("\f")
			case 'n':
				result.raw("\n")
			case 'r':
				result.raw("\r")
			case 't':
				result.raw("\t")
			case 'u':
				if ch3 := s.scanHexDigits(4); ch3 >= 0 {
					result.unit(ch3)
				} else {
					s.err = scanErrInvalidUnicode
				}
			default:
				s.err = scanErrInvalidEscapeCharacter
			}
			start = s.pos
			continue
		}
		if ch >= 0 && ch <= 0x1f {
			if isLineBreak(ch) {
				result.raw(s.text[start:s.pos])
				s.err = scanErrUnexpectedEndOfString
				break
			}
			s.err = scanErrInvalidCharacter
			// mark as error but continue with string
		}
		s.pos++
	}
	return result.String()
}

func (s *scanner) scan() syntaxKind {
	s.value = ""
	s.err = scanErrNone
	s.tokenOffset = s.pos
	n := len(s.text)
	if s.pos >= n {
		s.tokenOffset = n
		s.token = tokEOF
		return s.token
	}
	code := s.at(s.pos)
	if isWhiteSpace(code) {
		start := s.pos
		for {
			s.pos++
			code = s.at(s.pos)
			if !isWhiteSpace(code) {
				break
			}
		}
		s.value = s.text[start:s.pos]
		s.token = tokTrivia
		return s.token
	}
	if isLineBreak(code) {
		start := s.pos
		s.pos++
		if code == '\r' && s.at(s.pos) == '\n' {
			s.pos++
		}
		s.value = s.text[start:s.pos]
		s.token = tokLineBreak
		return s.token
	}
	switch code {
	case '{':
		s.pos++
		s.token = tokOpenBrace
		return s.token
	case '}':
		s.pos++
		s.token = tokCloseBrace
		return s.token
	case '[':
		s.pos++
		s.token = tokOpenBracket
		return s.token
	case ']':
		s.pos++
		s.token = tokCloseBracket
		return s.token
	case ':':
		s.pos++
		s.token = tokColon
		return s.token
	case ',':
		s.pos++
		s.token = tokComma
		return s.token
	case '"':
		s.pos++
		s.value = s.scanString()
		s.token = tokString
		return s.token
	case '/':
		start := s.pos
		if s.at(s.pos+1) == '/' {
			s.pos += 2
			for s.pos < n && !isLineBreak(s.at(s.pos)) {
				s.pos++
			}
			s.value = s.text[start:s.pos]
			s.token = tokLineComment
			return s.token
		}
		if s.at(s.pos+1) == '*' {
			s.pos += 2
			safeLength := n - 1 // for lookahead
			closed := false
			for s.pos < safeLength {
				ch := s.at(s.pos)
				if ch == '*' && s.at(s.pos+1) == '/' {
					s.pos += 2
					closed = true
					break
				}
				s.pos++
			}
			if !closed {
				s.pos++
				s.err = scanErrUnexpectedEndOfComment
			}
			s.value = substr(s.text, start, s.pos)
			s.token = tokBlockComment
			return s.token
		}
		s.value = "/"
		s.pos++
		s.token = tokUnknown
		return s.token
	case '-':
		s.value = "-"
		s.pos++
		if s.pos == n || !isDigit(s.at(s.pos)) {
			s.token = tokUnknown
			return s.token
		}
		s.value += s.scanNumber()
		s.token = tokNumber
		return s.token
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		s.value = s.scanNumber()
		s.token = tokNumber
		return s.token
	}
	for s.pos < n && isUnknownContentCharacter(code) {
		s.pos++
		code = s.at(s.pos)
	}
	if s.tokenOffset != s.pos {
		s.value = s.text[s.tokenOffset:s.pos]
		switch s.value {
		case "true":
			s.token = tokTrue
		case "false":
			s.token = tokFalse
		case "null":
			s.token = tokNull
		default:
			s.token = tokUnknown
		}
		return s.token
	}
	s.value = s.text[s.pos : s.pos+1]
	s.pos++
	s.token = tokUnknown
	return s.token
}

func isUnknownContentCharacter(code int) bool {
	if isWhiteSpace(code) || isLineBreak(code) {
		return false
	}
	switch code {
	case '}', ']', '{', '[', '"', ':', ',', '/':
		return false
	}
	return true
}

func isWhiteSpace(ch int) bool { return ch == ' ' || ch == '\t' }
func isLineBreak(ch int) bool  { return ch == '\n' || ch == '\r' }
func isDigit(ch int) bool      { return ch >= '0' && ch <= '9' }

// substr mirrors String.prototype.substring: arguments are clamped to
// [0, len(s)] and swapped when start > end.
func substr(s string, start, end int) string {
	start = clamp(start, 0, len(s))
	end = clamp(end, 0, len(s))
	if start > end {
		start, end = end, start
	}
	return s[start:end]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
