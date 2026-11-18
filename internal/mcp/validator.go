package mcp

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
)

// JSONSchemaValidator compiles a tool's outputSchema (the SDK's jsonSchemaValidator option;
// its default is an Ajv-backed validator).
type JSONSchemaValidator interface {
	// Compile returns a validator, or an error when the schema cannot be compiled.
	Compile(schema *jsonv.Object) (func(value any) (valid bool, errorMessage string), error)
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
func (BasicJSONSchemaValidator) Compile(schema *jsonv.Object) (func(any) (bool, string), error) {
	if err := precompile(schema); err != nil {
		return nil, err
	}
	return func(v any) (bool, string) {
		if msg := validateNode(schema, schema, v, ""); msg != "" {
			return false, msg
		}
		return true, ""
	}, nil
}

func precompile(node any) error {
	switch t := node.(type) {
	case *jsonv.Object:
		if p, ok := t.Get("pattern"); ok {
			if ps, ok := p.(string); ok {
				if _, err := regexp.Compile(ps); err != nil {
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
	case []any:
		for _, x := range t {
			if err := precompile(x); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonType(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case *jsonv.Object:
		return "object"
	}
	return "unknown"
}

func typeMatches(want string, v any) bool {
	got := jsonType(v)
	return want == got || (want == "number" && got == "integer")
}

func resolveRef(root *jsonv.Object, ref string) (any, bool) {
	if ref == "#" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var cur any = root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case *jsonv.Object:
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

func num(o *jsonv.Object, k string) (float64, bool) {
	v, ok := o.Get(k)
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func validateNode(root *jsonv.Object, node any, v any, path string) string {
	if b, ok := node.(bool); ok {
		if !b {
			return fmt.Sprintf("%s must NOT be valid", pathOrRoot(path))
		}
		return ""
	}
	s, ok := node.(*jsonv.Object)
	if !ok {
		return ""
	}
	if ref, ok := s.Get("$ref"); ok {
		if rs, ok := ref.(string); ok {
			if target, ok := resolveRef(root, rs); ok {
				if msg := validateNode(root, target, v, path); msg != "" {
					return msg
				}
			}
		}
	}
	if t, ok := s.Get("type"); ok {
		matched := false
		switch tv := t.(type) {
		case string:
			matched = typeMatches(tv, v)
		case []any:
			for _, x := range tv {
				if xs, ok := x.(string); ok && typeMatches(xs, v) {
					matched = true
				}
			}
		default:
			matched = true
		}
		if !matched {
			return fmt.Sprintf("%s must be %s", pathOrRoot(path), jsonv.MustStringify(t))
		}
	}
	if c, ok := s.Get("const"); ok && !jsonv.Equal(c, v) {
		return fmt.Sprintf("%s must be equal to constant", pathOrRoot(path))
	}
	if e, ok := s.Get("enum"); ok {
		if list, ok := e.([]any); ok {
			found := false
			for _, x := range list {
				if jsonv.Equal(x, v) {
					found = true
				}
			}
			if !found {
				return fmt.Sprintf("%s must be equal to one of the allowed values", pathOrRoot(path))
			}
		}
	}
	switch tv := v.(type) {
	case string:
		n := float64(utf8.RuneCountInString(tv))
		if m, ok := num(s, "minLength"); ok && n < m {
			return fmt.Sprintf("%s must NOT have fewer than %s characters", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "maxLength"); ok && n > m {
			return fmt.Sprintf("%s must NOT have more than %s characters", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if p, ok := s.Get("pattern"); ok {
			if ps, ok := p.(string); ok {
				if re, err := regexp.Compile(ps); err == nil && !re.MatchString(tv) {
					return fmt.Sprintf("%s must match pattern %s", pathOrRoot(path), jsonv.MustStringify(ps))
				}
			}
		}
	case float64:
		if m, ok := num(s, "minimum"); ok && tv < m {
			return fmt.Sprintf("%s must be >= %s", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "maximum"); ok && tv > m {
			return fmt.Sprintf("%s must be <= %s", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "exclusiveMinimum"); ok && tv <= m {
			return fmt.Sprintf("%s must be > %s", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "exclusiveMaximum"); ok && tv >= m {
			return fmt.Sprintf("%s must be < %s", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "multipleOf"); ok && m != 0 {
			q := tv / m
			if math.Abs(q-math.Round(q)) > 1e-9 {
				return fmt.Sprintf("%s must be multiple of %s", pathOrRoot(path), jsonv.FormatNumber(m))
			}
		}
	case []any:
		if m, ok := num(s, "minItems"); ok && float64(len(tv)) < m {
			return fmt.Sprintf("%s must NOT have fewer than %s items", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "maxItems"); ok && float64(len(tv)) > m {
			return fmt.Sprintf("%s must NOT have more than %s items", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if u, ok := s.Get("uniqueItems"); ok && u == true {
			for i := range tv {
				for j := i + 1; j < len(tv); j++ {
					if jsonv.Equal(tv[i], tv[j]) {
						return fmt.Sprintf("%s must NOT have duplicate items (items ## %d and %d are identical)", pathOrRoot(path), j, i)
					}
				}
			}
		}
		start := 0
		if pi, ok := s.Get("prefixItems"); ok {
			if list, ok := pi.([]any); ok {
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
	case *jsonv.Object:
		if m, ok := num(s, "minProperties"); ok && float64(tv.Len()) < m {
			return fmt.Sprintf("%s must NOT have fewer than %s properties", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if m, ok := num(s, "maxProperties"); ok && float64(tv.Len()) > m {
			return fmt.Sprintf("%s must NOT have more than %s properties", pathOrRoot(path), jsonv.FormatNumber(m))
		}
		if r, ok := s.Get("required"); ok {
			if list, ok := r.([]any); ok {
				for _, x := range list {
					if k, ok := x.(string); ok && !tv.Has(k) {
						return fmt.Sprintf("%s must have required property '%s'", pathOrRoot(path), k)
					}
				}
			}
		}
		props, _ := s.Get("properties")
		propObj, _ := props.(*jsonv.Object)
		pp, _ := s.Get("patternProperties")
		ppObj, _ := pp.(*jsonv.Object)
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
					if ap == false {
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
		if list, ok := all.([]any); ok {
			for _, sub := range list {
				if msg := validateNode(root, sub, v, path); msg != "" {
					return msg
				}
			}
		}
	}
	if anyOf, ok := s.Get("anyOf"); ok {
		if list, ok := anyOf.([]any); ok {
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
		if list, ok := oneOf.([]any); ok {
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
