package weburl

import (
	"strconv"
	"strings"
)

type hostKind uint8

const (
	hostDomain hostKind = iota // a domain, an opaque host, or the empty host
	hostIPv4
	hostIPv6
)

type host struct {
	kind hostKind
	name string    // hostDomain
	ipv4 uint32    // hostIPv4
	ipv6 [8]uint16 // hostIPv6
}

func (h host) String() string {
	switch h.kind {
	case hostIPv4:
		return serializeIPv4(h.ipv4)
	case hostIPv6:
		return "[" + serializeIPv6(h.ipv6) + "]"
	}
	return h.name
}

func isForbiddenHostCodePoint(c rune) bool {
	switch c {
	case 0x00, '\t', '\n', '\r', ' ', '#', '/', ':', '<', '>', '?', '@', '[', '\\', ']', '^', '|':
		return true
	}
	return false
}

func isForbiddenDomainCodePoint(c rune) bool {
	return isForbiddenHostCodePoint(c) || c <= 0x1f || c == '%' || c == 0x7f
}

// parseHost is the URL Standard's host parser, as ada implements it.
func parseHost(input string, special bool) (host, bool) {
	if input == "" {
		return host{}, false
	}
	if input[0] == '[' {
		if input[len(input)-1] != ']' {
			return host{}, false
		}
		addr, ok := parseIPv6(input[1 : len(input)-1])
		return host{kind: hostIPv6, ipv6: addr}, ok
	}
	if !special {
		return parseOpaqueHost(input)
	}
	domain := percentDecode(input)
	ascii, ok := idnaToASCII(domain)
	if !ok {
		return host{}, false
	}
	for _, c := range ascii {
		if isForbiddenDomainCodePoint(c) {
			return host{}, false
		}
	}
	if endsInNumber(ascii) {
		addr, ok := parseIPv4(ascii)
		return host{kind: hostIPv4, ipv4: addr}, ok
	}
	return host{name: ascii}, true
}

func parseOpaqueHost(input string) (host, bool) {
	for _, c := range input {
		if c != '%' && isForbiddenHostCodePoint(c) {
			return host{}, false
		}
	}
	return host{name: percentEncodeString(input, c0ControlSet)}, true
}

// endsInNumber is ada's checkers::is_ipv4, which agrees with the standard's "ends in a number"
// on the lowercase ASCII domains it is given.
func endsInNumber(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		return false
	}
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return false
	}
	if allBytes(s, isDigit) {
		return true
	}
	if len(s) < 2 || s[:2] != "0x" {
		return false
	}
	return allBytes(s[2:], func(c byte) bool { return isDigit(c) || (c >= 'a' && c <= 'f') })
}

func allBytes(s string, f func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !f(s[i]) {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func parseIPv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := 10
	switch {
	case len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X"):
		s, base = s[2:], 16
	case len(s) >= 2 && s[0] == '0':
		s, base = s[1:], 8
	}
	if s == "" {
		return 0, true
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		d, ok := digitValue(s[i])
		if !ok || d >= base {
			return 0, false
		}
		v = v*uint64(base) + uint64(d)
		if v > 1<<32 {
			v = 1 << 33 // too large either way; stop growing
		}
	}
	return v, true
}

func digitValue(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

func parseIPv4(input string) (uint32, bool) {
	parts := strings.Split(input, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return 0, false
	}
	numbers := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseIPv4Number(p)
		if !ok {
			return 0, false
		}
		numbers[i] = n
	}
	for _, n := range numbers[:len(numbers)-1] {
		if n > 255 {
			return 0, false
		}
	}
	last := numbers[len(numbers)-1]
	if last >= 1<<(8*(5-len(numbers))) {
		return 0, false
	}
	ipv4 := last
	for i, n := range numbers[:len(numbers)-1] {
		ipv4 += n << (8 * (3 - i))
	}
	return uint32(ipv4), true
}

func serializeIPv4(a uint32) string {
	return strconv.Itoa(int(a>>24)) + "." + strconv.Itoa(int(a>>16&0xff)) + "." +
		strconv.Itoa(int(a>>8&0xff)) + "." + strconv.Itoa(int(a&0xff))
}

func parseIPv6(in string) ([8]uint16, bool) {
	var addr [8]uint16
	input := []rune(in)
	pieceIndex, compress, p := 0, -1, 0
	at := func(i int) rune {
		if i < len(input) {
			return input[i]
		}
		return -1
	}
	if at(p) == ':' {
		if at(p+1) != ':' {
			return addr, false
		}
		p += 2
		pieceIndex++
		compress = pieceIndex
	}
	for at(p) != -1 {
		if pieceIndex == 8 {
			return addr, false
		}
		if at(p) == ':' {
			if compress != -1 {
				return addr, false
			}
			p++
			pieceIndex++
			compress = pieceIndex
			continue
		}
		value, length := 0, 0
		for length < 4 {
			c := at(p)
			if c < 0 || c > 0x7f {
				break
			}
			d, ok := digitValue(byte(c))
			if !ok {
				break
			}
			value = value*0x10 + d
			p++
			length++
		}
		if at(p) == '.' {
			if length == 0 {
				return addr, false
			}
			p -= length
			if pieceIndex > 6 {
				return addr, false
			}
			numbersSeen := 0
			for at(p) != -1 {
				ipv4Piece := -1
				if numbersSeen > 0 {
					if at(p) == '.' && numbersSeen < 4 {
						p++
					} else {
						return addr, false
					}
				}
				if c := at(p); c < '0' || c > '9' {
					return addr, false
				}
				for c := at(p); c >= '0' && c <= '9'; c = at(p) {
					n := int(c - '0')
					switch {
					case ipv4Piece == -1:
						ipv4Piece = n
					case ipv4Piece == 0:
						return addr, false
					default:
						ipv4Piece = ipv4Piece*10 + n
					}
					if ipv4Piece > 255 {
						return addr, false
					}
					p++
				}
				addr[pieceIndex] = addr[pieceIndex]*0x100 + uint16(ipv4Piece)
				numbersSeen++
				if numbersSeen == 2 || numbersSeen == 4 {
					pieceIndex++
				}
			}
			if numbersSeen != 4 {
				return addr, false
			}
			break
		} else if at(p) == ':' {
			p++
			if at(p) == -1 {
				return addr, false
			}
		} else if at(p) != -1 {
			return addr, false
		}
		addr[pieceIndex] = uint16(value)
		pieceIndex++
	}
	if compress != -1 {
		swaps := pieceIndex - compress
		pieceIndex = 7
		for pieceIndex != 0 && swaps > 0 {
			addr[pieceIndex], addr[compress+swaps-1] = addr[compress+swaps-1], addr[pieceIndex]
			pieceIndex--
			swaps--
		}
	} else if pieceIndex != 8 {
		return addr, false
	}
	return addr, true
}

func serializeIPv6(addr [8]uint16) string {
	// Compress the first longest run of two or more zero pieces.
	best, bestLen := -1, 1
	for i := 0; i < 8; {
		if addr[i] != 0 {
			i++
			continue
		}
		j := i
		for j < 8 && addr[j] == 0 {
			j++
		}
		if j-i > bestLen {
			best, bestLen = i, j-i
		}
		i = j
	}
	var b strings.Builder
	ignore0 := false
	for i := 0; i < 8; i++ {
		if ignore0 && addr[i] == 0 {
			continue
		}
		ignore0 = false
		if i == best {
			if i == 0 {
				b.WriteString("::")
			} else {
				b.WriteString(":")
			}
			ignore0 = true
			continue
		}
		b.WriteString(strconv.FormatUint(uint64(addr[i]), 16))
		if i != 7 {
			b.WriteString(":")
		}
	}
	return b.String()
}
