package toml

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
)

// Stringify is smol-toml's stringify(obj) with default options (maxDepth
// 1000, numbersAsFloat false). v must be an object. Keys are emitted in
// JavaScript key order; nested objects become [tables], non-empty arrays
// whose elements are all objects become [[arrays of tables]], everything
// else is written inline. Null and undefined members of tables are skipped;
// null or undefined inside arrays, and lone surrogates in keys, are errors
// (smol-toml throws). Lone surrogates in string values become U+FFFD.
func Stringify(v jsjson.Value) (s string, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(stringifyError)
			if !ok {
				panic(r)
			}
			s, err = "", e.err
		}
	}()
	if extendedTypeOf(v) != "object" {
		return "", errors.New("stringify can only be called with an object")
	}
	str := stringifyTable("", v, "", 1000)
	if !strings.HasSuffix(str, "\n") {
		str += "\n"
	}
	return str, nil
}

type stringifyError struct{ err error }

func throw(msg string) { panic(stringifyError{errors.New(msg)}) }

func extendedTypeOf(v jsjson.Value) string {
	switch v.(type) {
	case nil:
		return "undefined"
	case jsjson.Null:
		throw("Cannot read properties of null (reading 'getUTCDate')")
	case jsjson.Bool:
		return "boolean"
	case jsjson.Number:
		return "number"
	case jsjson.String:
		return "string"
	case jsjson.Array:
		return "array"
	case *Date:
		return "date"
	}
	return "object"
}

// members lists an object's own keys and values (none for extensions).
func members(v jsjson.Value) []jsjson.Member {
	if o, ok := v.(*jsjson.Object); ok {
		return o.Members()
	}
	return nil
}

func isArrayOfTables(a jsjson.Array) bool {
	for _, e := range a {
		if extendedTypeOf(e) != "object" {
			return false
		}
	}
	return len(a) != 0
}

func formatString(s string) string {
	return strings.ReplaceAll(jsjson.Quote(jsjson.ToWellFormed(s)), "\x7f", `\u007f`)
}

func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func formatKey(s string) string {
	if isBareKey(s) {
		return s
	}
	if !jsjson.IsWellFormed(s) {
		panic(stringifyError{errors.New("key contains illegal lone surrogates")})
	}
	return strings.ReplaceAll(jsjson.Quote(s), "\x7f", `\u007f`)
}

func formatNumber(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	if x == math.Trunc(x) && math.Abs(x) > 1<<53-1 {
		// toFixed(1)
		if math.Abs(x) >= 1e21 {
			return jsjson.FormatNumber(x)
		}
		return strconv.FormatFloat(x, 'f', 1, 64)
	}
	return jsjson.FormatNumber(x)
}

func stringifyValue(v jsjson.Value, typ string, depth int) string {
	if depth == 0 {
		throw("Could not stringify the object: maximum object depth exceeded")
	}
	switch typ {
	case "number":
		return formatNumber(float64(v.(jsjson.Number)))
	case "boolean":
		if v.(jsjson.Bool) {
			return "true"
		}
		return "false"
	case "string":
		return formatString(string(v.(jsjson.String)))
	case "date":
		s, ok := v.(*Date).ISOString()
		if !ok {
			throw("Invalid time value")
		}
		return s
	case "object":
		return stringifyInlineTable(v, depth)
	case "array":
		return stringifyArray(v.(jsjson.Array), depth)
	}
	return "undefined" // JavaScript concatenates the undefined result
}

func stringifyInlineTable(v jsjson.Value, depth int) string {
	ms := members(v)
	if len(ms) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteString("{ ")
	for i, m := range ms {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(formatKey(m.Key))
		b.WriteString(" = ")
		b.WriteString(stringifyValue(m.Value, extendedTypeOf(m.Value), depth-1))
	}
	b.WriteString(" }")
	return b.String()
}

func stringifyArray(a jsjson.Array, depth int) string {
	if len(a) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteString("[ ")
	for i, e := range a {
		if i > 0 {
			b.WriteString(", ")
		}
		switch e.(type) {
		case nil, jsjson.Null:
			throw("arrays cannot contain null or undefined values")
		}
		b.WriteString(stringifyValue(e, extendedTypeOf(e), depth-1))
	}
	b.WriteString(" ]")
	return b.String()
}

func stringifyArrayTable(a jsjson.Array, key string, depth int) string {
	if depth == 0 {
		throw("Could not stringify the object: maximum object depth exceeded")
	}
	res := ""
	for _, e := range a {
		if res != "" {
			res += "\n"
		}
		res += "[[" + key + "]]\n"
		res += stringifyTable("", e, key, depth)
	}
	return res
}

func stringifyTable(tableKey string, obj jsjson.Value, prefix string, depth int) string {
	if depth == 0 {
		throw("Could not stringify the object: maximum object depth exceeded")
	}
	preamble, tables := "", ""
	join := func(key string) string {
		if prefix != "" {
			return prefix + "." + key
		}
		return key
	}
	for _, m := range members(obj) {
		switch m.Value.(type) {
		case nil, jsjson.Null:
			continue
		}
		typ := extendedTypeOf(m.Value)
		key := formatKey(m.Key)
		if arr, ok := m.Value.(jsjson.Array); ok && isArrayOfTables(arr) {
			if tables != "" {
				tables += "\n"
			}
			tables += stringifyArrayTable(arr, join(key), depth-1)
		} else if typ == "object" {
			if tables != "" {
				tables += "\n"
			}
			tblKey := join(key)
			tables += stringifyTable(tblKey, m.Value, tblKey, depth-1)
		} else {
			preamble += key + " = " + stringifyValue(m.Value, typ, depth) + "\n"
		}
	}
	if tableKey != "" && (preamble != "" || tables == "") {
		if preamble != "" {
			preamble = "[" + tableKey + "]\n" + preamble
		} else {
			preamble = "[" + tableKey + "]"
		}
	}
	if preamble != "" && tables != "" {
		return preamble + "\n" + tables
	}
	if preamble != "" {
		return preamble
	}
	return tables
}
