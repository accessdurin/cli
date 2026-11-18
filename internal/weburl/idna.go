package weburl

import (
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

//go:generate go run gen_idna_tables.go

// This file ports ada's IDNA processing (src/ada_idna.cpp at the version gen_idna_tables.go
// names), which is what Node's URL runs for domain to ASCII: UTS #46 with CheckHyphens=false,
// CheckBidi=true, CheckJoiners=true, UseSTD3ASCIIRules=false, Nontransitional_Processing and
// VerifyDnsLength=false. It is a port rather than a call into golang.org/x/net/idna because
// the two differ on many inputs (punycode validation of xn-- labels, the Bidi rule, ContextJ),
// and the CLI must reach the same verdict on a host that Node does. ada's MIT license notice,
// which covers this port too, is at the top of idna_tables.go.

type direction uint8

// The order matches ada's enum direction, which the generated table relies on.
const (
	dirNONE direction = iota
	dirBN
	dirCS
	dirES
	dirON
	dirEN
	dirL
	dirR
	dirNSM
	dirAL
	dirAN
	dirET
	dirWS
	dirRLO
	dirLRO
	dirPDF
	dirRLE
	dirRLI
	dirFSI
	dirPDI
	dirLRI
	dirB
	dirS
	dirLRE
)

type dirRange struct {
	start, end uint32
	dir        direction
}

// idnaMap applies the UTS #46 mapping; ok is false when the input holds a disallowed code point.
func idnaMap(in []rune) (out []rune, ok bool) {
	out = make([]rune, 0, len(in))
	for _, r := range in {
		// The table's first range starts at U+0000, so i is never negative.
		i := sort.Search(len(idnaTable), func(i int) bool { return idnaTable[i][0] > uint32(r) }) - 1
		d := idnaTable[i][1]
		switch uint8(d) {
		case 0: // ignored
		case 1: // valid
			out = append(out, r)
		case 2: // disallowed
			return nil, false
		default: // mapped
			count, index := int(d>>24), int(uint16(d>>8))
			for _, m := range idnaMappings[index : index+count] {
				out = append(out, rune(m))
			}
		}
	}
	return out, true
}

func nfc(in []rune) []rune {
	return []rune(norm.NFC.String(string(in)))
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsRune(table []uint32, r rune) bool {
	i := sort.Search(len(table), func(i int) bool { return table[i] >= uint32(r) })
	return i < len(table) && table[i] == uint32(r)
}

func findDirection(r rune) direction {
	c := uint32(r)
	i := sort.Search(len(idnaDirTable), func(i int) bool { return idnaDirTable[i].end >= c })
	if i == len(idnaDirTable) || c < idnaDirTable[i].start {
		return dirNONE
	}
	return idnaDirTable[i].dir
}

// labelValid is ada's is_label_valid, quirks included: a joiner that passes its ContextJ rule
// ends the check early, and the Bidi rule looks only at the label itself.
func labelValid(label []rune) bool {
	if len(label) == 0 {
		return true
	}
	if containsRune(idnaCombining[:], label[0]) {
		return false
	}
	for i, c := range label {
		switch c {
		case 0x200c:
			if i > 0 && containsRune(idnaVirama[:], label[i-1]) {
				return true
			}
			if i == 0 || i+1 >= len(label) {
				return false
			}
			lOrD := func(r rune) bool { return containsRune(idnaL[:], r) || containsRune(idnaD[:], r) }
			rOrD := func(r rune) bool { return containsRune(idnaR[:], r) || containsRune(idnaD[:], r) }
			before, after := false, false
			for _, r := range label[:i] {
				before = before || lOrD(r)
			}
			for _, r := range label[i+1:] {
				after = after || rOrD(r)
			}
			return before && after
		case 0x200d:
			return i > 0 && containsRune(idnaVirama[:], label[i-1])
		}
	}

	last := -1
	for i := len(label) - 1; i >= 0; i-- {
		if findDirection(label[i]) != dirNSM {
			last = i
			break
		}
	}
	if last < 0 {
		return false
	}
	rtl := false
	for _, r := range label {
		switch findDirection(r) {
		case dirR, dirAL, dirAN:
			rtl = true
		}
	}
	if !rtl {
		return true
	}
	if findDirection(label[0]) == dirL {
		// ada checks only the code points before the last non-NSM one here.
		for i := 0; i < last; i++ {
			switch findDirection(label[i]) {
			case dirL, dirEN, dirES, dirCS, dirET, dirON, dirBN, dirNSM:
			default:
				return false
			}
		}
		return true
	}
	hasAN, hasEN := false, false
	for i := 0; i <= last; i++ {
		d := findDirection(label[i])
		if d == dirEN {
			hasEN = true
			if hasAN {
				return false
			}
		}
		if d == dirAN {
			hasAN = true
			if hasEN {
				return false
			}
		}
		switch d {
		case dirR, dirAL, dirAN, dirEN, dirES, dirCS, dirET, dirON, dirBN, dirNSM:
		default:
			return false
		}
		if i == last {
			switch d {
			case dirR, dirAL, dirAN, dirEN:
			default:
				return false
			}
		}
	}
	return true
}

const (
	punyBase        = 36
	punyTMin        = 1
	punyTMax        = 26
	punySkew        = 38
	punyDamp        = 700
	punyInitialBias = 72
	punyInitialN    = 128
	maxInt32        = 0x7fffffff
)

func punyAdapt(d, n int32, first bool) int32 {
	if first {
		d /= punyDamp
	} else {
		d /= 2
	}
	d += d / n
	k := int32(0)
	for d > ((punyBase-punyTMin)*punyTMax)/2 {
		d /= punyBase - punyTMin
		k += punyBase
	}
	return k + ((punyBase-punyTMin+1)*d)/(d+punySkew)
}

func punyThreshold(k, bias int32) int32 {
	switch {
	case k <= bias:
		return punyTMin
	case k >= bias+punyTMax:
		return punyTMax
	default:
		return k - bias
	}
}

// punycodeDecode is ada's punycode_to_utf32: lowercase digits only, 32-bit overflow checks.
func punycodeDecode(input string) ([]rune, bool) {
	var out []rune
	n := uint32(punyInitialN)
	i := int32(0)
	bias := int32(punyInitialBias)
	if end := strings.LastIndexByte(input, '-'); end >= 0 {
		for j := 0; j < end; j++ {
			if input[j] >= 0x80 {
				return nil, false
			}
			out = append(out, rune(input[j]))
		}
		input = input[end+1:]
	}
	for len(input) > 0 {
		oldi := i
		w := int32(1)
		for k := int32(punyBase); ; k += punyBase {
			if len(input) == 0 {
				return nil, false
			}
			c := input[0]
			input = input[1:]
			var digit int32
			switch {
			case c >= 'a' && c <= 'z':
				digit = int32(c - 'a')
			case c >= '0' && c <= '9':
				digit = int32(c-'0') + 26
			default:
				return nil, false
			}
			if digit > (maxInt32-i)/w {
				return nil, false
			}
			i += digit * w
			t := punyThreshold(k, bias)
			if digit < t {
				break
			}
			if w > maxInt32/(punyBase-t) {
				return nil, false
			}
			w *= punyBase - t
		}
		written := int32(len(out))
		bias = punyAdapt(i-oldi, written+1, oldi == 0)
		if i/(written+1) > int32(maxInt32-n) {
			return nil, false
		}
		n += uint32(i / (written + 1))
		i %= written + 1
		if n < 0x80 {
			return nil, false
		}
		out = append(out, 0)
		copy(out[i+1:], out[i:])
		out[i] = rune(n)
		i++
	}
	return out, true
}

// punycodeEncode is ada's utf32_to_punycode.
func punycodeEncode(input []rune, out *strings.Builder) bool {
	n := uint32(punyInitialN)
	d := int32(0)
	bias := int32(punyInitialBias)
	h := 0
	for _, c := range input {
		if c < 0x80 {
			h++
			out.WriteByte(byte(c))
		}
		if c > 0x10ffff || (c >= 0xd880 && c < 0xe000) {
			return false
		}
	}
	b := h
	if b > 0 {
		out.WriteByte('-')
	}
	digit := func(d int32) byte {
		if d < 26 {
			return byte(d + 97)
		}
		return byte(d + 22)
	}
	for h < len(input) {
		m := uint32(0x10FFFF)
		for _, c := range input {
			if uint32(c) >= n && uint32(c) < m {
				m = uint32(c)
			}
		}
		if int64(m-n) > int64(maxInt32-d)/int64(h+1) {
			return false
		}
		d += int32((m - n) * uint32(h+1))
		n = m
		for _, c := range input {
			if uint32(c) < n {
				if d == maxInt32 {
					return false
				}
				d++
			}
			if uint32(c) == n {
				q := d
				for k := int32(punyBase); ; k += punyBase {
					t := punyThreshold(k, bias)
					if q < t {
						break
					}
					out.WriteByte(digit(t + (q-t)%(punyBase-t)))
					q = (q - t) / (punyBase - t)
				}
				out.WriteByte(digit(q))
				bias = punyAdapt(d, int32(h+1), h == b)
				d = 0
				h++
			}
		}
		d++
		n++
	}
	return true
}

// checkPunycodeLabel validates an xn-- label's decoded form as ada does.
func checkPunycodeLabel(puny string) bool {
	decoded, ok := punycodeDecode(puny)
	if !ok {
		return false
	}
	mapped, ok := idnaMap(decoded)
	if !ok || !equalRunes(decoded, mapped) {
		return false
	}
	if !equalRunes(nfc(mapped), mapped) || len(mapped) == 0 {
		return false
	}
	return labelValid(mapped)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// idnaToASCII is ada::idna::to_ascii; ok is false where ada returns its empty error string.
func idnaToASCII(domain string) (string, bool) {
	var out strings.Builder
	if isASCII(domain) {
		mapped := asciiLower(domain)
		labels := strings.Split(mapped, ".")
		for li, label := range labels {
			if strings.HasPrefix(label, "xn--") && !checkPunycodeLabel(label[4:]) {
				return "", false
			}
			out.WriteString(label)
			if li < len(labels)-1 {
				out.WriteByte('.')
			}
		}
		return out.String(), out.Len() > 0
	}
	if !utf8.ValidString(domain) {
		return "", false
	}
	mapped, ok := idnaMap([]rune(domain))
	if !ok {
		return "", false
	}
	mapped = nfc(mapped)
	if len(mapped) == 0 {
		return "", false
	}
	var labels [][]rune
	start := 0
	for i, r := range mapped {
		if r == '.' {
			labels = append(labels, mapped[start:i])
			start = i + 1
		}
	}
	labels = append(labels, mapped[start:])
	// "a." splits into "a" and a final empty label, which adds nothing, as in ada.
	for li, label := range labels {
		switch {
		case len(label) == 0:
		case len(label) >= 4 && string(label[:4]) == "xn--":
			for _, r := range label {
				if r >= 0x80 {
					return "", false
				}
			}
			out.WriteString(string(label))
			if !checkPunycodeLabel(string(label[4:])) {
				return "", false
			}
		default:
			ascii := true
			for _, r := range label {
				if r >= 0x80 {
					ascii = false
					break
				}
			}
			if ascii {
				out.WriteString(string(label))
				break
			}
			if !labelValid(label) {
				return "", false
			}
			out.WriteString("xn--")
			if !punycodeEncode(label, &out) {
				return "", false
			}
		}
		if li < len(labels)-1 {
			out.WriteByte('.')
		}
	}
	return out.String(), out.Len() > 0
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}
