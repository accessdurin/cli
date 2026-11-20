package mcp

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/accessdurin/cli/internal/jsjson"
)

// JSONSchemaValidator compiles a tool's outputSchema (the SDK's jsonSchemaValidator option;
// its default is an Ajv-backed validator).
type JSONSchemaValidator interface {
	// Compile returns a validator, or an error when the schema cannot be compiled.
	Compile(schema *jsjson.Object) (func(value jsjson.Value) (valid bool, errorMessage string), error)
}

// BasicJSONSchemaValidator is the port's default JSONSchemaValidator. It is NOT Ajv: it checks
// type, enum, const, properties, required, additionalProperties, patternProperties, items,
// prefixItems, minItems/maxItems, uniqueItems, minLength/maxLength, pattern, minimum/maximum,
// exclusiveMinimum/exclusiveMaximum, multipleOf, minProperties/maxProperties, allOf, anyOf, oneOf,
// not, if/then/else and local "#/..." $ref; other keywords (formats, $dynamicRef, remote refs,
// unevaluated*) are accepted without checking. The CLI never reaches output validation (each
// tool call runs on a fresh connection without a cached tools/list), so this only matters for
// other users of the port.
type BasicJSONSchemaValidator struct{}

// Compile implements JSONSchemaValidator.
func (BasicJSONSchemaValidator) Compile(schema *jsjson.Object) (func(jsjson.Value) (bool, string), error) {
	if err := precompile(schema); err != nil {
		return nil, err
	}
	return func(v jsjson.Value) (bool, string) {
		if msg := validateNode(schema, schema, v, ""); msg != "" {
			return false, msg
		}
		return true, ""
	}, nil
}

func precompile(node jsjson.Value) error {
	switch t := node.(type) {
	case *jsjson.Object:
		if p, ok := t.Get("pattern"); ok {
			if ps, ok := p.(jsjson.String); ok {
				if _, err := regexp.Compile(string(ps)); err != nil {
					return fmt.Errorf("pattern %q is invalid: %v", ps, err)
				}
			}
		}
		for _, k := range t.Keys() {
			v, _ := t.Get(k)
			if k == "const" || k == "enum" || k == "default" || k == "examples" {
				continue
			}
			if err := precompile(v); err != nil {
				return err
			}
		}
	case jsjson.Array:
		for _, x := range t {
			if err := precompile(x); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonType(v jsjson.Value) string {
	switch t := v.(type) {
	case jsjson.Null:
		return "null"
	case jsjson.Bool:
		return "boolean"
	case jsjson.Number:
		if f := float64(t); f == math.Trunc(f) && !math.IsInf(f, 0) {
			return "integer"
		}
		return "number"
	case jsjson.String:
		return "string"
	case jsjson.Array:
		return "array"
	case *jsjson.Object:
		return "object"
	}
	return "unknown"
}

func typeMatches(want string, v jsjson.Value) bool {
	got := jsonType(v)
	return want == got || (want == "number" && got == "integer")
}

func resolveRef(root *jsjson.Object, ref string) (jsjson.Value, bool) {
	if ref == "#" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var cur jsjson.Value = root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case *jsjson.Object:
			next, ok := c.Get(part)
			if !ok {
				return nil, false
			}
			cur = next
		default:
			return nil, false
		}
	}
	return cur, true
}

func num(o *jsjson.Object, k string) (float64, bool) {
	v, ok := o.Get(k)
	if !ok {
		return 0, false
	}
	f, ok := v.(jsjson.Number)
	return float64(f), ok
}

func validateNode(root *jsjson.Object, node jsjson.Value, v jsjson.Value, path string) string {
	if b, ok := node.(jsjson.Bool); ok {
		if !b {
			return fmt.Sprintf("%s must NOT be valid", pathOrRoot(path))
		}
		return ""
	}
	s, ok := node.(*jsjson.Object)
	if !ok {
		return ""
	}
	if ref, ok := s.Get("$ref"); ok {
		if rs, ok := ref.(jsjson.String); ok {
			if target, ok := resolveRef(root, string(rs)); ok {
				if msg := validateNode(root, target, v, path); msg != "" {
					return msg
				}
			}
		}
	}
	if t, ok := s.Get("type"); ok {
		matched := false
		switch tv := t.(type) {
		case jsjson.String:
			matched = typeMatches(string(tv), v)
		case jsjson.Array:
			for _, x := range tv {
				if xs, ok := x.(jsjson.String); ok && typeMatches(string(xs), v) {
					matched = true
				}
			}
		default:
			matched = true
		}
		if !matched {
			return fmt.Sprintf("%s must be %s", pathOrRoot(path), jsjson.Stringify(t))
		}
	}
	if c, ok := s.Get("const"); ok && !jsjson.Equal(c, v) {
		return fmt.Sprintf("%s must be equal to constant", pathOrRoot(path))
	}
	if e, ok := s.Get("enum"); ok {
		if list, ok := e.(jsjson.Array); ok {
			found := false
			for _, x := range list {
				if jsjson.Equal(x, v) {
					found = true
				}
			}
			if !found {
				return fmt.Sprintf("%s must be equal to one of the allowed values", pathOrRoot(path))
			}
		}
	}
	switch tv := v.(type) {
	case jsjson.String:
		n := float64(utf8.RuneCountInString(string(tv)))
		if m, ok := num(s, "minLength"); ok && n < m {
			return fmt.Sprintf("%s must NOT have fewer than %s characters", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "maxLength"); ok && n > m {
			return fmt.Sprintf("%s must NOT have more than %s characters", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if p, ok := s.Get("pattern"); ok {
			if ps, ok := p.(jsjson.String); ok {
				if re, err := regexp.Compile(string(ps)); err == nil && !re.MatchString(string(tv)) {
					return fmt.Sprintf("%s must match pattern %s", pathOrRoot(path), jsjson.Quote(string(ps)))
				}
			}
		}
	case jsjson.Number:
		f := float64(tv)
		if m, ok := num(s, "minimum"); ok && f < m {
			return fmt.Sprintf("%s must be >= %s", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "maximum"); ok && f > m {
			return fmt.Sprintf("%s must be <= %s", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "exclusiveMinimum"); ok && f <= m {
			return fmt.Sprintf("%s must be > %s", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "exclusiveMaximum"); ok && f >= m {
			return fmt.Sprintf("%s must be < %s", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "multipleOf"); ok && m != 0 {
			q := f / m
			if math.Abs(q-math.Round(q)) > 1e-9 {
				return fmt.Sprintf("%s must be multiple of %s", pathOrRoot(path), jsjson.FormatNumber(m))
			}
		}
	case jsjson.Array:
		if m, ok := num(s, "minItems"); ok && float64(len(tv)) < m {
			return fmt.Sprintf("%s must NOT have fewer than %s items", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "maxItems"); ok && float64(len(tv)) > m {
			return fmt.Sprintf("%s must NOT have more than %s items", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if u, ok := s.Get("uniqueItems"); ok && u == jsjson.Bool(true) {
			for i := range tv {
				for j := i + 1; j < len(tv); j++ {
					if jsjson.Equal(tv[i], tv[j]) {
						return fmt.Sprintf("%s must NOT have duplicate items (items ## %d and %d are identical)", pathOrRoot(path), j, i)
					}
				}
			}
		}
		start := 0
		if pi, ok := s.Get("prefixItems"); ok {
			if list, ok := pi.(jsjson.Array); ok {
				for i, sub := range list {
					if i < len(tv) {
						if msg := validateNode(root, sub, tv[i], fmt.Sprintf("%s/%d", path, i)); msg != "" {
							return msg
						}
					}
				}
				start = len(list)
			}
		}
		if items, ok := s.Get("items"); ok {
			for i := start; i < len(tv); i++ {
				if msg := validateNode(root, items, tv[i], fmt.Sprintf("%s/%d", path, i)); msg != "" {
					return msg
				}
			}
		}
	case *jsjson.Object:
		if m, ok := num(s, "minProperties"); ok && float64(tv.Len()) < m {
			return fmt.Sprintf("%s must NOT have fewer than %s properties", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if m, ok := num(s, "maxProperties"); ok && float64(tv.Len()) > m {
			return fmt.Sprintf("%s must NOT have more than %s properties", pathOrRoot(path), jsjson.FormatNumber(m))
		}
		if r, ok := s.Get("required"); ok {
			if list, ok := r.(jsjson.Array); ok {
				for _, x := range list {
					if k, ok := x.(jsjson.String); ok && !tv.Has(string(k)) {
						return fmt.Sprintf("%s must have required property '%s'", pathOrRoot(path), k)
					}
				}
			}
		}
		props, _ := s.Get("properties")
		propObj, _ := props.(*jsjson.Object)
		pp, _ := s.Get("patternProperties")
		ppObj, _ := pp.(*jsjson.Object)
		for _, k := range tv.Keys() {
			val, _ := tv.Get(k)
			matched := false
			if sub, ok := propObj.Get(k); ok {
				matched = true
				if msg := validateNode(root, sub, val, path+"/"+k); msg != "" {
					return msg
				}
			}
			for _, pat := range ppObj.Keys() {
				if re, err := regexp.Compile(pat); err == nil && re.MatchString(k) {
					matched = true
					sub, _ := ppObj.Get(pat)
					if msg := validateNode(root, sub, val, path+"/"+k); msg != "" {
						return msg
					}
				}
			}
			if !matched {
				if ap, ok := s.Get("additionalProperties"); ok {
					if ap == jsjson.Bool(false) {
						return fmt.Sprintf("%s must NOT have additional properties", pathOrRoot(path))
					}
					if msg := validateNode(root, ap, val, path+"/"+k); msg != "" {
						return msg
					}
				}
			}
		}
	}
	if all, ok := s.Get("allOf"); ok {
		if list, ok := all.(jsjson.Array); ok {
			for _, sub := range list {
				if msg := validateNode(root, sub, v, path); msg != "" {
					return msg
				}
			}
		}
	}
	if anyOf, ok := s.Get("anyOf"); ok {
		if list, ok := anyOf.(jsjson.Array); ok {
			passed := false
			for _, sub := range list {
				if validateNode(root, sub, v, path) == "" {
					passed = true
					break
				}
			}
			if !passed {
				return fmt.Sprintf("%s must match a schema in anyOf", pathOrRoot(path))
			}
		}
	}
	if oneOf, ok := s.Get("oneOf"); ok {
		if list, ok := oneOf.(jsjson.Array); ok {
			count := 0
			for _, sub := range list {
				if validateNode(root, sub, v, path) == "" {
					count++
				}
			}
			if count != 1 {
				return fmt.Sprintf("%s must match exactly one schema in oneOf", pathOrRoot(path))
			}
		}
	}
	if not, ok := s.Get("not"); ok {
		if validateNode(root, not, v, path) == "" {
			return fmt.Sprintf("%s must NOT be valid", pathOrRoot(path))
		}
	}
	if cond, ok := s.Get("if"); ok {
		if validateNode(root, cond, v, path) == "" {
			if then, ok := s.Get("then"); ok {
				if msg := validateNode(root, then, v, path); msg != "" {
					return msg
				}
			}
		} else if els, ok := s.Get("else"); ok {
			if msg := validateNode(root, els, v, path); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func pathOrRoot(path string) string {
	if path == "" {
		return "data"
	}
	return "data" + path
}
