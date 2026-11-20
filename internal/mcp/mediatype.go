package mcp

import (
	"encoding/base64"
	"regexp"
	"strings"
)

var (
	ctTypeRegexp  = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+/[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	ctParamRegexp = regexp.MustCompile(`^; *([!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+) *= *("(?:[\x0b\x20\x21\x23-\x5b\x5d-\x7e\x{80}-\x{ff}]|\\[\x0b\x20-\x{ff}])*"|[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+) *`)
)

// parseContentType is content-type@1.0.5 parse(): it returns the lowercased type or false.
func parseContentType(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	index := strings.IndexByte(header, ';')
	var typ string
	if index != -1 {
		typ = strings.TrimSpace(header[:index])
	} else {
		typ = strings.TrimSpace(header)
	}
	if !ctTypeRegexp.MatchString(typ) {
		return "", false
	}
	if index != -1 {
		rest := header[index:]
		for rest != "" {
			m := ctParamRegexp.FindStringIndex(rest)
			if m == nil || m[0] != 0 {
				return "", false
			}
			rest = rest[m[1]:]
		}
	}
	return strings.ToLower(typ), true
}

// MediaTypeEssence is mediaTypeEssence: the lowercased media type of a Content-Type header, or
// "" when absent or unusable.
func MediaTypeEssence(header string) string {
	if header == "" {
		return ""
	}
	if t, ok := parseContentType(header); ok {
		return t
	}
	before, _, _ := strings.Cut(header, ";")
	essence := strings.ToLower(strings.TrimSpace(before))
	if essence == "" || strings.Contains(header[len(essence):], ",") {
		return ""
	}
	return essence
}

const (
	base64SentinelPrefix = "=?base64?"
	base64SentinelSuffix = "?="
)

func needsBase64(s string) bool {
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, base64SentinelPrefix) && strings.HasSuffix(s, base64SentinelSuffix) {
		return true
	}
	if s != strings.TrimFunc(s, isJSSpaceRune) {
		return true
	}
	for _, c := range s {
		if c == 9 || (c >= 32 && c <= 126) {
			continue
		}
		return true
	}
	return false
}

func isJSSpaceRune(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// EncodeMcpParamValue is encodeMcpParamValue (SEP-2243 header value encoding).
func EncodeMcpParamValue(value string) string {
	if !needsBase64(value) {
		return value
	}
	return base64SentinelPrefix + base64.StdEncoding.EncodeToString([]byte(value)) + base64SentinelSuffix
}
