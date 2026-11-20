package toml

import (
	"math"
	"strconv"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
)

// Date is smol-toml's TomlDate: a JavaScript Date that remembers whether it
// carries a date, a time and an offset. Like the original it serializes to
// JSON through toISOString, so jsjson.Stringify writes the TOML-style ISO
// text, and smol-toml's stringify writes it bare.
type Date struct {
	jsjson.Extension
	ms      float64 // JavaScript time value (always valid)
	hasDate bool
	hasTime bool
	offset  string // "" for local; "Z" or the authored "+hh:mm"/"-hh:mm"
	local   bool
}

const (
	dateOffset        = 1
	dateLocalDateTime = 2
	dateLocalDate     = 3
	dateLocalTime     = 4
)

// IsDateTime, IsLocal, IsDate and IsTime mirror TomlDate's methods.
func (d *Date) IsDateTime() bool { return d.hasDate && d.hasTime }

// IsLocal reports whether the value has no offset.
func (d *Date) IsLocal() bool { return !d.hasDate || !d.hasTime || d.local }

// IsDate reports a local date without time.
func (d *Date) IsDate() bool { return d.hasDate && !d.hasTime }

// IsTime reports a local time without date.
func (d *Date) IsTime() bool { return d.hasTime && !d.hasDate }

// Time returns the JavaScript time value (milliseconds since the epoch).
func (d *Date) Time() float64 { return d.ms }

// ToJSON implements jsjson.JSONer like Date#toJSON.
func (d *Date) ToJSON() jsjson.Value {
	s, ok := d.ISOString()
	if !ok {
		return nil // toISOString would throw a RangeError
	}
	return jsjson.String(s)
}

// ISOString is TomlDate#toISOString. ok is false where JavaScript throws.
func (d *Date) ISOString() (string, bool) {
	iso, ok := jsISOString(d.ms)
	if !ok {
		return "", false
	}
	switch {
	case d.IsDate():
		return jsSlice(iso, 0, 10), true
	case d.IsTime():
		return jsSlice(iso, 11, 23), true
	case d.local:
		return iso[:len(iso)-1], true
	case d.offset == "Z" || d.offset == "z":
		return iso, true
	}
	o := d.offset
	minutes := jsNumber(jsSlice(o, 1, 3))*60 + jsNumber(jsSlice(o, 4, 6))
	if o[0] != '-' {
		minutes = -minutes
	}
	shifted, ok := jsISOString(timeClip(d.ms - minutes*60e3))
	if !ok {
		return "", false
	}
	return shifted[:len(shifted)-1] + o, true
}

// newTomlDate runs TomlDate's constructor for the fast types used by the
// parser and returns nil when the date is invalid.
func newTomlDate(date []uint16, fasttype int, unsafeDelim bool) *Date {
	d := &Date{offset: "Z"}
	s := date
	invalid := false
	if fasttype < 3 {
		if jsNumber(jsjson.FromUTF16(sliceUnits(s, 11, 13))) > 23 {
			invalid = true
		} else {
			if fasttype == dateLocalDateTime {
				d.local = true
				d.offset = ""
				s = append(append([]uint16(nil), s...), 'Z')
			} else if c := s[len(s)-1]; c != 'Z' && c != 'z' {
				d.offset = jsjson.FromUTF16(sliceUnits(s, len(s)-6, len(s)))
			}
			if unsafeDelim {
				t := append([]uint16(nil), sliceUnits(s, 0, 10)...)
				t = append(t, 'T')
				s = append(t, sliceUnits(s, 11, len(s))...)
			}
		}
	} else if fasttype == dateLocalTime {
		if jsNumber(jsjson.FromUTF16(sliceUnits(s, 0, 2))) > 23 {
			invalid = true
		} else {
			t := []uint16{'0', '0', '0', '0', '-', '0', '1', '-', '0', '1', 'T'}
			t = append(t, s...)
			s = append(t, 'Z')
		}
	}
	if invalid {
		return nil
	}
	ms := parseJSDate(s)
	if math.IsNaN(ms) {
		return nil
	}
	d.ms = ms
	d.hasDate = fasttype != dateLocalTime
	d.hasTime = fasttype != dateLocalDate
	return d
}

func sliceUnits(s []uint16, from, to int) []uint16 {
	if from < 0 {
		from = max(len(s)+from, 0)
	}
	from, to = min(from, len(s)), min(to, len(s))
	if to < from {
		return nil
	}
	return s[from:to]
}

// jsSlice is String#slice on ASCII text.
func jsSlice(s string, from, to int) string {
	from, to = min(from, len(s)), min(to, len(s))
	if to < from {
		return ""
	}
	return s[from:to]
}

// jsNumber is the unary + (ToNumber) on a short string.
func jsNumber(s string) float64 {
	s = strings.TrimFunc(s, func(r rune) bool { return isJSWhiteSpace(r) || isLineTerminator(r) })
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			v, err := strconv.ParseUint(s[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(v)
		}
	}
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return math.NaN()
	}
	switch body {
	case "Infinity":
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	digits, dot, exp := 0, false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot && !exp:
			dot = true
		case (c == 'e' || c == 'E') && !exp && digits > 0:
			exp = true
			if i+1 < len(body) && (body[i+1] == '+' || body[i+1] == '-') {
				i++
			}
			if i+1 >= len(body) {
				return math.NaN()
			}
		default:
			return math.NaN()
		}
	}
	if digits == 0 {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// ---------------------------------------------------------------- JS Date

const msPerDay = 86400000

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// daysFromCivil returns days since 1970-01-01 for a proleptic Gregorian date.
func daysFromCivil(y, m, d int64) int64 {
	if m <= 2 {
		y--
	}
	era := floorDiv(y, 400)
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

func civilFromDays(z int64) (y, m, d int64) {
	z += 719468
	era := floorDiv(z, 146097)
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y = yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d = doy - (153*mp+2)/5 + 1
	if mp < 10 {
		m = mp + 3
	} else {
		m = mp - 9
	}
	if m <= 2 {
		y++
	}
	return
}

// makeDay is ECMAScript MakeDay with a 0-based month.
func makeDay(year, month, day int64) float64 {
	if year < -1000000 || year > 1000000 {
		return math.NaN()
	}
	y := year + floorDiv(month, 12)
	m := month - floorDiv(month, 12)*12
	return float64(daysFromCivil(y, m+1, 1) + day - 1)
}

func timeClip(t float64) float64 {
	if math.IsNaN(t) || math.IsInf(t, 0) || math.Abs(t) > 8.64e15 {
		return math.NaN()
	}
	return math.Trunc(t) + 0
}

// jsISOString is Date#toISOString; ok is false for an invalid date.
func jsISOString(t float64) (string, bool) {
	if math.IsNaN(t) {
		return "", false
	}
	ms := int64(t)
	days := floorDiv(ms, msPerDay)
	rem := ms - days*msPerDay
	y, mo, d := civilFromDays(days)
	var b strings.Builder
	switch {
	case y >= 0 && y <= 9999:
		b.WriteString(pad(y, 4))
	case y < 0:
		b.WriteString("-" + pad(-y, 6))
	default:
		b.WriteString("+" + pad(y, 6))
	}
	b.WriteString("-" + pad(mo, 2) + "-" + pad(d, 2) + "T")
	b.WriteString(pad(rem/3600000, 2) + ":" + pad(rem/60000%60, 2) + ":" + pad(rem/1000%60, 2) + "." + pad(rem%1000, 3) + "Z")
	return b.String(), true
}

func pad(n int64, w int) string {
	s := strconv.FormatInt(n, 10)
	for len(s) < w {
		s = "0" + s
	}
	return s
}

// ---------------------------------------------------------------- V8 DateParser
//
// A port of V8's src/date/dateparser*.{h,cc}: ES5 ISO strings first, then the
// legacy (Safari-compatible) parser for whatever is left. Times without an
// offset are local time in V8; this port treats local time as UTC, which is
// what the fixtures were generated with (TZ=UTC). Only whether a date is
// valid can matter to the CLI, and that does not depend on the time zone.

const kNone = math.MaxInt32

const (
	tokInvalid = iota
	tokUnknown
	tokNumber
	tokSymbol
	tokWhiteSpace
	tokKeyword
	tokEnd
)

const (
	kwInvalid = iota
	kwMonthName
	kwTimeZoneName
	kwTimeSeparator
	kwAmPm
)

type dateToken struct {
	tag    int
	value  int // number, symbol char or keyword value
	length int
	kw     int // keyword type
}

func (t dateToken) isNumber() bool      { return t.tag == tokNumber }
func (t dateToken) isSymbol(c int) bool { return t.tag == tokSymbol && t.value == c }
func (t dateToken) isAsciiSign() bool {
	return t.tag == tokSymbol && (t.value == '+' || t.value == '-')
}
func (t dateToken) asciiSign() int                 { return 44 - t.value }
func (t dateToken) isFixedLengthNumber(n int) bool { return t.tag == tokNumber && t.length == n }
func (t dateToken) isKeywordType(k int) bool       { return t.tag == tokKeyword && t.kw == k }
func (t dateToken) isKeywordZ() bool {
	return t.tag == tokKeyword && t.kw == kwTimeZoneName && t.length == 1 && t.value == 0
}

var keywordTable = []struct {
	prefix [3]rune
	kw     int
	value  int
}{
	{[3]rune{'j', 'a', 'n'}, kwMonthName, 1}, {[3]rune{'f', 'e', 'b'}, kwMonthName, 2},
	{[3]rune{'m', 'a', 'r'}, kwMonthName, 3}, {[3]rune{'a', 'p', 'r'}, kwMonthName, 4},
	{[3]rune{'m', 'a', 'y'}, kwMonthName, 5}, {[3]rune{'j', 'u', 'n'}, kwMonthName, 6},
	{[3]rune{'j', 'u', 'l'}, kwMonthName, 7}, {[3]rune{'a', 'u', 'g'}, kwMonthName, 8},
	{[3]rune{'s', 'e', 'p'}, kwMonthName, 9}, {[3]rune{'o', 'c', 't'}, kwMonthName, 10},
	{[3]rune{'n', 'o', 'v'}, kwMonthName, 11}, {[3]rune{'d', 'e', 'c'}, kwMonthName, 12},
	{[3]rune{'a', 'm', 0}, kwAmPm, 0}, {[3]rune{'p', 'm', 0}, kwAmPm, 12},
	{[3]rune{'u', 't', 0}, kwTimeZoneName, 0}, {[3]rune{'u', 't', 'c'}, kwTimeZoneName, 0},
	{[3]rune{'z', 0, 0}, kwTimeZoneName, 0}, {[3]rune{'g', 'm', 't'}, kwTimeZoneName, 0},
	{[3]rune{'c', 'd', 't'}, kwTimeZoneName, -5}, {[3]rune{'c', 's', 't'}, kwTimeZoneName, -6},
	{[3]rune{'e', 'd', 't'}, kwTimeZoneName, -4}, {[3]rune{'e', 's', 't'}, kwTimeZoneName, -5},
	{[3]rune{'m', 'd', 't'}, kwTimeZoneName, -6}, {[3]rune{'m', 's', 't'}, kwTimeZoneName, -7},
	{[3]rune{'p', 'd', 't'}, kwTimeZoneName, -7}, {[3]rune{'p', 's', 't'}, kwTimeZoneName, -8},
	{[3]rune{'t', 0, 0}, kwTimeSeparator, 0},
}

func lookupKeyword(pre [3]rune, length int) (kw, value int) {
	for _, e := range keywordTable {
		if e.prefix == pre && (length <= 3 || e.kw == kwMonthName) {
			return e.kw, e.value
		}
	}
	return kwInvalid, 0
}

func isJSWhiteSpace(c rune) bool {
	switch c {
	case 0x09, 0x0B, 0x0C, 0x20, 0xA0, 0xFEFF, 0x1680, 0x202F, 0x205F, 0x3000:
		return true
	}
	return c >= 0x2000 && c <= 0x200A
}

func isLineTerminator(c rune) bool { return c == 0x0A || c == 0x0D || c == 0x2028 || c == 0x2029 }

type dateScanner struct {
	s    []uint16
	i    int // index of the next char after ch
	ch   rune
	next dateToken
}

func newDateScanner(s []uint16) *dateScanner {
	sc := &dateScanner{s: s}
	sc.advance()
	sc.next = sc.scan()
	return sc
}

func (sc *dateScanner) advance() {
	if sc.i < len(sc.s) {
		sc.ch = rune(sc.s[sc.i])
	} else {
		sc.ch = 0
	}
	sc.i++
}

func (sc *dateScanner) isDigit() bool { return sc.ch >= '0' && sc.ch <= '9' }

func (sc *dateScanner) scan() dateToken {
	pre := sc.i
	if sc.ch == 0 {
		return dateToken{tag: tokEnd}
	}
	if sc.isDigit() {
		n, i := 0, 0
		for sc.ch == '0' {
			sc.advance()
		}
		for sc.isDigit() {
			if i < 9 {
				n = n*10 + int(sc.ch-'0')
			}
			i++
			sc.advance()
		}
		return dateToken{tag: tokNumber, value: n, length: sc.i - pre}
	}
	switch sc.ch {
	case ':', '-', '+', '.', ')':
		c := sc.ch
		sc.advance()
		return dateToken{tag: tokSymbol, value: int(c)}
	}
	if sc.ch >= 'A' && !isJSWhiteSpace(sc.ch) {
		var buf [3]rune
		n := 0
		for ; sc.ch >= 'A' && !isJSWhiteSpace(sc.ch); sc.advance() {
			if n < 3 {
				buf[n] = sc.ch | 0x20
			}
			n++
		}
		kw, v := lookupKeyword(buf, n)
		return dateToken{tag: tokKeyword, kw: kw, value: v, length: n}
	}
	if isJSWhiteSpace(sc.ch) || isLineTerminator(sc.ch) {
		sc.advance()
		return dateToken{tag: tokWhiteSpace, length: 1}
	}
	if sc.ch == '(' {
		balance := 0
		for {
			if sc.ch == ')' {
				balance--
			} else if sc.ch == '(' {
				balance++
			}
			sc.advance()
			if !(balance > 0 && sc.ch != 0) {
				break
			}
		}
		return dateToken{tag: tokUnknown}
	}
	sc.advance()
	return dateToken{tag: tokUnknown}
}

func (sc *dateScanner) Next() dateToken {
	t := sc.next
	sc.next = sc.scan()
	return t
}

func (sc *dateScanner) Peek() dateToken { return sc.next }

func (sc *dateScanner) SkipSymbol(c int) bool {
	if sc.next.isSymbol(c) {
		sc.Next()
		return true
	}
	return false
}

func between(x, lo, hi int) bool { return x >= lo && x <= hi }
func isMonth(x int) bool         { return between(x, 1, 12) }
func isDay(x int) bool           { return between(x, 1, 31) }
func isHour(x int) bool          { return between(x, 0, 23) }
func isMinute(x int) bool        { return between(x, 0, 59) }
func isSecond(x int) bool        { return between(x, 0, 59) }

type dayComposer struct {
	comp       [3]int
	index      int
	namedMonth int
	isISO      bool
}

func (d *dayComposer) add(n int) bool {
	if d.index == 3 {
		return false
	}
	d.comp[d.index] = n
	d.index++
	return true
}

func (d *dayComposer) write(out *[8]float64) bool {
	if d.index < 1 {
		return false
	}
	for d.index < 3 {
		d.comp[d.index] = 1
		d.index++
	}
	year, month, day := 0, kNone, kNone
	if d.namedMonth == kNone {
		if d.isISO || (d.index == 3 && !isDay(d.comp[0])) {
			year, month, day = d.comp[0], d.comp[1], d.comp[2]
		} else {
			month, day = d.comp[0], d.comp[1]
			if d.index == 3 {
				year = d.comp[2]
			}
		}
	} else {
		month = d.namedMonth
		if d.index == 1 {
			day = d.comp[0]
		} else if !isDay(d.comp[0]) {
			year, day = d.comp[0], d.comp[1]
		} else {
			day, year = d.comp[0], d.comp[1]
		}
	}
	if !d.isISO {
		if between(year, 0, 49) {
			year += 2000
		} else if between(year, 50, 99) {
			year += 1900
		}
	}
	if year < -(1<<31) || year > 1<<31-1 || !isMonth(month) || !isDay(day) {
		return false
	}
	out[0], out[1], out[2] = float64(year), float64(month-1), float64(day)
	return true
}

type timeComposer struct {
	comp       [4]int
	index      int
	hourOffset int
}

func (t *timeComposer) isEmpty() bool { return t.index == 0 }
func (t *timeComposer) isExpecting(n int) bool {
	return (t.index == 1 && isMinute(n)) || (t.index == 2 && isSecond(n)) || (t.index == 3 && between(n, 0, 999))
}
func (t *timeComposer) add(n int) bool {
	if t.index < 4 {
		t.comp[t.index] = n
		t.index++
		return true
	}
	return false
}
func (t *timeComposer) addFinal(n int) bool {
	if !t.add(n) {
		return false
	}
	for t.index < 4 {
		t.comp[t.index] = 0
		t.index++
	}
	return true
}

func (t *timeComposer) write(out *[8]float64) bool {
	for t.index < 4 {
		t.comp[t.index] = 0
		t.index++
	}
	hour, minute, second, ms := t.comp[0], t.comp[1], t.comp[2], t.comp[3]
	if t.hourOffset != kNone {
		if !between(hour, 0, 12) {
			return false
		}
		hour %= 12
		hour += t.hourOffset
	}
	if !isHour(hour) || !isMinute(minute) || !isSecond(second) || !between(ms, 0, 999) {
		if hour != 24 || minute != 0 || second != 0 || ms != 0 {
			return false
		}
	}
	out[3], out[4], out[5], out[6] = float64(hour), float64(minute), float64(second), float64(ms)
	return true
}

type tzComposer struct{ sign, hour, minute int }

func (z *tzComposer) set(offsetHours int) {
	z.sign = 1
	if offsetHours < 0 {
		z.sign = -1
	}
	z.hour = offsetHours * z.sign
	z.minute = 0
}
func (z *tzComposer) setSign(s int) {
	z.sign = 1
	if s < 0 {
		z.sign = -1
	}
}
func (z *tzComposer) isExpecting(n int) bool {
	return z.hour != kNone && z.minute == kNone && isMinute(n)
}
func (z *tzComposer) isUTC() bool   { return z.hour == 0 && z.minute == 0 }
func (z *tzComposer) isEmpty() bool { return z.hour == kNone }

func (z *tzComposer) write(out *[8]float64) bool {
	if z.sign != kNone {
		if z.hour == kNone {
			z.hour = 0
		}
		if z.minute == kNone {
			z.minute = 0
		}
		total := uint64(uint32(z.hour)*3600 + uint32(z.minute)*60)
		if total > 1<<31-1 {
			return false
		}
		t := int(total)
		if z.sign < 0 {
			t = -t
		}
		out[7] = float64(t)
	} else {
		out[7] = math.NaN()
	}
	return true
}

func readMilliseconds(t dateToken) int {
	number, length := t.value, t.length
	if length < 3 {
		if length == 1 {
			number *= 100
		} else if length == 2 {
			number *= 10
		}
	} else if length > 3 {
		if length > 9 {
			length = 9
		}
		factor := 1
		for {
			factor *= 10
			length--
			if length <= 3 {
				break
			}
		}
		number /= factor
	}
	return number
}

// parseES5 is ParseES5DateTime; it returns the first token it did not handle.
func parseES5(sc *dateScanner, day *dayComposer, tm *timeComposer, tz *tzComposer) dateToken {
	if sc.Peek().isAsciiSign() {
		sign := sc.Next()
		if !sc.Peek().isFixedLengthNumber(6) {
			return sign
		}
		s := sign.asciiSign()
		year := sc.Next().value
		if s < 0 && year == 0 {
			return sign
		}
		day.add(s * year)
	} else if sc.Peek().isFixedLengthNumber(4) {
		day.add(sc.Next().value)
	} else {
		return sc.Next()
	}
	if sc.SkipSymbol('-') {
		if !sc.Peek().isFixedLengthNumber(2) || !isMonth(sc.Peek().value) {
			return sc.Next()
		}
		day.add(sc.Next().value)
		if sc.SkipSymbol('-') {
			if !sc.Peek().isFixedLengthNumber(2) || !isDay(sc.Peek().value) {
				return sc.Next()
			}
			day.add(sc.Next().value)
		}
	}
	invalid := dateToken{tag: tokInvalid}
	if !sc.Peek().isKeywordType(kwTimeSeparator) {
		if sc.Peek().tag != tokEnd {
			return sc.Next()
		}
	} else {
		sc.Next()
		if !sc.Peek().isFixedLengthNumber(2) || !between(sc.Peek().value, 0, 24) {
			return invalid
		}
		hourIs24 := sc.Peek().value == 24
		tm.add(sc.Next().value)
		if !sc.SkipSymbol(':') {
			return invalid
		}
		if !sc.Peek().isFixedLengthNumber(2) || !isMinute(sc.Peek().value) || (hourIs24 && sc.Peek().value > 0) {
			return invalid
		}
		tm.add(sc.Next().value)
		if sc.SkipSymbol(':') {
			if !sc.Peek().isFixedLengthNumber(2) || !isSecond(sc.Peek().value) || (hourIs24 && sc.Peek().value > 0) {
				return invalid
			}
			tm.add(sc.Next().value)
			if sc.SkipSymbol('.') {
				if !sc.Peek().isNumber() || (hourIs24 && sc.Peek().value > 0) {
					return invalid
				}
				tm.add(readMilliseconds(sc.Next()))
			}
		}
		if sc.Peek().isKeywordZ() {
			sc.Next()
			tz.set(0)
		} else if sc.Peek().isSymbol('+') || sc.Peek().isSymbol('-') {
			if sc.Next().value == '+' {
				tz.setSign(1)
			} else {
				tz.setSign(-1)
			}
			if sc.Peek().isFixedLengthNumber(4) {
				hm := sc.Next().value
				h, m := hm/100, hm%100
				if !isHour(h) || !isMinute(m) {
					return invalid
				}
				tz.hour, tz.minute = h, m
			} else {
				if !sc.Peek().isFixedLengthNumber(2) || !isHour(sc.Peek().value) {
					return invalid
				}
				tz.hour = sc.Next().value
				if !sc.SkipSymbol(':') {
					return invalid
				}
				if !sc.Peek().isFixedLengthNumber(2) || !isMinute(sc.Peek().value) {
					return invalid
				}
				tz.minute = sc.Next().value
			}
		}
		if sc.Peek().tag != tokEnd {
			return invalid
		}
	}
	if tz.isEmpty() && tm.isEmpty() {
		tz.set(0)
	}
	day.isISO = true
	return dateToken{tag: tokEnd}
}

// parseJSDate is Date.parse (new Date(string).getTime()) with TZ=UTC.
func parseJSDate(s []uint16) float64 {
	sc := newDateScanner(s)
	day := &dayComposer{namedMonth: kNone}
	tm := &timeComposer{hourOffset: kNone}
	tz := &tzComposer{sign: kNone, hour: kNone, minute: kNone}
	token := parseES5(sc, day, tm, tz)
	if token.tag == tokInvalid {
		return math.NaN()
	}
	hasReadNumber := day.index != 0
	for ; token.tag != tokEnd; token = sc.Next() {
		switch {
		case token.isNumber():
			hasReadNumber = true
			n := token.value
			if sc.SkipSymbol(':') {
				if sc.SkipSymbol(':') {
					if !tm.isEmpty() {
						return math.NaN()
					}
					tm.add(n)
					tm.add(0)
				} else {
					if !tm.add(n) {
						return math.NaN()
					}
					if sc.Peek().isSymbol('.') {
						sc.Next()
					}
				}
			} else if sc.SkipSymbol('.') && tm.isExpecting(n) {
				tm.add(n)
				if !sc.Peek().isNumber() {
					return math.NaN()
				}
				ms := readMilliseconds(sc.Next())
				if ms < 0 {
					return math.NaN()
				}
				tm.addFinal(ms)
			} else if tz.isExpecting(n) {
				tz.minute = n
			} else if tm.isExpecting(n) {
				tm.addFinal(n)
				p := sc.Peek()
				if p.tag != tokEnd && p.tag != tokWhiteSpace && !p.isKeywordZ() && !p.isAsciiSign() {
					return math.NaN()
				}
			} else {
				if !day.add(n) {
					return math.NaN()
				}
				sc.SkipSymbol('-')
			}
		case token.tag == tokKeyword:
			if token.kw == kwAmPm && !tm.isEmpty() {
				tm.hourOffset = token.value
			} else if token.kw == kwMonthName {
				day.namedMonth = token.value
				sc.SkipSymbol('-')
			} else if token.kw == kwTimeZoneName && hasReadNumber {
				tz.set(token.value)
			} else {
				if hasReadNumber {
					return math.NaN()
				}
				if sc.Peek().isNumber() {
					return math.NaN()
				}
			}
		case token.isAsciiSign() && (tz.isUTC() || !tm.isEmpty()):
			tz.setSign(token.asciiSign())
			n, length := 0, 0
			if sc.Peek().isNumber() {
				t := sc.Next()
				length, n = t.length, t.value
			}
			hasReadNumber = true
			switch {
			case sc.Peek().isSymbol(':'):
				tz.hour, tz.minute = n, kNone
			case length == 1 || length == 2:
				tz.hour, tz.minute = n, 0
			case length == 3 || length == 4:
				tz.hour, tz.minute = n/100, n%100
			default:
				return math.NaN()
			}
		case (token.isAsciiSign() || token.isSymbol(')')) && hasReadNumber:
			return math.NaN()
		}
	}
	var out [8]float64
	if !day.write(&out) || !tm.write(&out) || !tz.write(&out) {
		return math.NaN()
	}
	dayNum := makeDay(int64(out[0]), int64(out[1]), int64(out[2]))
	t := out[3]*3600000 + out[4]*60000 + out[5]*1000 + out[6]
	date := dayNum*msPerDay + t
	if math.IsNaN(out[7]) {
		if date < -8.64e15-864e6 || date > 8.64e15+864e6 {
			return math.NaN()
		}
	} else {
		date -= out[7] * 1000
		if date < -8.64e15 || date > 8.64e15 {
			return math.NaN()
		}
	}
	return timeClip(date)
}
