package schema

import (
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/jsurl"
)

// URL is z.url() / z.string().url(): the trimmed input must satisfy URL.canParse, and the output
// is the trimmed input with ASCII tab, LF and CR removed (zod 4.6.5 $ZodURL).
func URL() *stringSchema {
	return String().with(func(v string) (string, []Issue) {
		trimmed := strings.TrimFunc(v, isJSWhitespace)
		if !jsurl.CanParse(trimmed) {
			return v, issue("invalid_format", "Invalid URL")
		}
		return strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(trimmed), nil
	})
}

// SafeURL is the SDK's SafeUrlSchema: a URL that is not javascript:, data: or vbscript:.
func SafeURL() *stringSchema {
	return URL().with(func(v string) (string, []Issue) {
		u, err := jsurl.Parse(v, nil)
		if err != nil {
			return v, issue("custom", "URL must be parseable")
		}
		switch u.Protocol() {
		case "javascript:", "data:", "vbscript:":
			return v, issue("custom", "URL cannot use javascript:, data:, or vbscript: scheme")
		}
		return v, nil
	})
}

// OptionalSafeURL is the SDK's OptionalSafeUrlSchema: SafeURL, absent, or "" (read as absent).
func OptionalSafeURL() Schema {
	return Union(Optional(SafeURL()), Transform(Literal(""), func(any) (any, error) { return Undefined, nil }))
}

const dateSource = `(?:(?:\d\d[2468][048]|\d\d[13579][26]|\d\d0[48]|[02468][048]00|[13579][26]00)-02-29|\d{4}-(?:(?:0[13578]|1[02])-(?:0[1-9]|[12]\d|3[01])|(?:0[469]|11)-(?:0[1-9]|[12]\d|30)|(?:02)-(?:0[1-9]|1\d|2[0-8])))`

var datetimeOffset = regexp.MustCompile(`^` + dateSource + `T(?:(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d(?:\.\d+)?(?:Z|([+-](?:[01]\d|2[0-3]):[0-5]\d)))$`)

// ISODateTimeOffset is z.iso.datetime({ offset: true }).
func ISODateTimeOffset() *stringSchema {
	return String().with(func(v string) (string, []Issue) {
		if datetimeOffset.MatchString(v) {
			return v, nil
		}
		return v, issue("invalid_format", "Invalid ISO datetime")
	})
}

// Base64 is the SDK's Base64Schema: a string that `atob` accepts (forgiving-base64 decode).
func Base64() *stringSchema {
	return String().with(func(v string) (string, []Issue) {
		if Atob(v) {
			return v, nil
		}
		return v, issue("custom", "Invalid Base64 string")
	})
}

// Atob reports whether the WHATWG forgiving-base64 decoder (atob) accepts s.
func Atob(s string) bool {
	var b strings.Builder
	for _, r := range s {
		if r > 0xFF {
			return false
		}
		switch r {
		case '\t', '\n', '\f', '\r', ' ':
			continue
		}
		b.WriteRune(r)
	}
	data := b.String()
	if len(data)%4 == 0 {
		if strings.HasSuffix(data, "==") {
			data = data[:len(data)-2]
		} else if strings.HasSuffix(data, "=") {
			data = data[:len(data)-1]
		}
	}
	if len(data)%4 == 1 {
		return false
	}
	for i := 0; i < len(data); i++ {
		c := data[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/') {
			return false
		}
	}
	return true
}
