package weburl

import "strings"

// IsZodURL reports whether zod 4's z.url() (with no options, as the CLI's contracts use it)
// accepts value. zod 4.1 runs new URL(value.trim()) and accepts whatever parses: any scheme,
// no hostname or protocol rule.
func IsZodURL(value string) bool {
	_, ok := ZodURL(value)
	return ok
}

// ZodURL is z.url().safeParse(value): on success it returns the parsed output, which is the
// input with JavaScript white space trimmed from both ends (not the normalized href).
func ZodURL(value string) (string, bool) {
	trimmed := strings.TrimFunc(value, isJSWhiteSpace)
	if !CanParse(trimmed) {
		return "", false
	}
	return trimmed, true
}

// isJSWhiteSpace is the set String.prototype.trim removes: ECMAScript WhiteSpace (TAB, VT, FF,
// SPACE, NBSP, ZWNBSP and the Space_Separator category) and LineTerminator (LF, CR, LS, PS).
func isJSWhiteSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f,
		0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
