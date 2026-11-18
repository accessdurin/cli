// Package schema reproduces the parts of zod v4 (4.6.5, the version the MCP SDK resolves) that
// the SDK's wire schemas use, operating on jsonv values.
//
// The SDK parses every message and result with zod and hands the *parsed output* onward, so its
// observable behavior includes zod's output semantics, which this package reproduces:
//
//   - object output lists declared keys first, in declaration order, then (for loose/catchall
//     objects) undeclared keys in input order; strip objects drop undeclared keys; strict objects
//     reject them;
//   - optional keys that are absent stay absent; defaults fill absent keys; catch replaces a
//     failing value (a present key caught to undefined keeps its slot as jsonv.Undefined, which
//     JSON.stringify omits);
//   - unions return the first option that parses;
//   - numbers must be finite, `.int()` means a safe integer, z.coerce.number applies Number().
//
// Issue messages approximate zod's but are not byte-identical (see Error).
package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
)

// Undefined is the value a schema sees for an absent key and may return to mean "undefined".
var Undefined = jsonv.Undefined

// Issue is one validation problem.
type Issue struct {
	Code    string `json:"code"`
	Path    []any  `json:"path"`
	Message string `json:"message"`
}

// Error is a failed parse (zod's ZodError).
type Error struct{ Issues []Issue }

// Error renders the issues as a JSON array, like ZodError#message (which pretty-prints the
// issue list). Field sets and wording differ from zod.
func (e *Error) Error() string {
	b, _ := json.MarshalIndent(e.Issues, "", "  ")
	return string(b)
}

// Schema is a parser.
type Schema interface {
	run(v any) (any, []Issue)
	opt() (optin, optout string)
}

// Parse runs s and returns the parsed value or an *Error.
func Parse(s Schema, v any) (any, error) {
	out, issues := s.run(v)
	if len(issues) > 0 {
		return nil, &Error{Issues: issues}
	}
	return out, nil
}

// Valid reports whether v parses.
func Valid(s Schema, v any) bool {
	_, issues := s.run(v)
	return len(issues) == 0
}

func issue(code, msg string) []Issue { return []Issue{{Code: code, Path: []any{}, Message: msg}} }

func invalidType(expected string, v any) []Issue {
	got := jsonv.TypeName(v)
	if f, ok := v.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		got = jsonv.FormatNumber(f)
		if math.IsNaN(f) {
			got = "NaN"
		} else if f > 0 {
			got = "Infinity"
		} else {
			got = "-Infinity"
		}
	}
	return issue("invalid_type", fmt.Sprintf("Invalid input: expected %s, received %s", expected, got))
}

func prefix(key any, issues []Issue) []Issue {
	out := make([]Issue, len(issues))
	for i, is := range issues {
		is.Path = append([]any{key}, is.Path...)
		out[i] = is
	}
	return out
}

type base struct{ in, out string }

func (b base) opt() (string, string) { return b.in, b.out }

// ---- primitives ----

type stringSchema struct {
	base
	checks []func(string) (string, []Issue)
}

// String is z.string().
func String() *stringSchema { return &stringSchema{} }

func (s *stringSchema) run(v any) (any, []Issue) {
	str, ok := v.(string)
	if !ok {
		return v, invalidType("string", v)
	}
	var all []Issue
	for _, c := range s.checks {
		next, issues := c(str)
		if len(issues) > 0 {
			all = append(all, issues...)
			continue
		}
		str = next
	}
	if len(all) > 0 {
		return str, all
	}
	return str, nil
}

func (s *stringSchema) with(c func(string) (string, []Issue)) *stringSchema {
	return &stringSchema{checks: append(append([]func(string) (string, []Issue){}, s.checks...), c)}
}

// Refine adds a predicate check.
func (s *stringSchema) Refine(pred func(string) bool, msg string) *stringSchema {
	return s.with(func(v string) (string, []Issue) {
		if pred(v) {
			return v, nil
		}
		return v, issue("custom", msg)
	})
}

// Regex adds a pattern check.
func (s *stringSchema) Regex(match func(string) bool, msg string) *stringSchema {
	return s.Refine(match, msg)
}

type numberSchema struct {
	base
	coerce bool
	isInt  bool
	min    *float64
	max    *float64
}

// Number is z.number().
func Number() *numberSchema { return &numberSchema{} }

// CoerceNumber is z.coerce.number().
func CoerceNumber() *numberSchema { return &numberSchema{coerce: true} }

// Int restricts to safe integers (zod v4 `.int()`).
func (n *numberSchema) Int() *numberSchema { c := *n; c.isInt = true; return &c }

// Min adds an inclusive lower bound.
func (n *numberSchema) Min(v float64) *numberSchema { c := *n; c.min = &v; return &c }

// Max adds an inclusive upper bound.
func (n *numberSchema) Max(v float64) *numberSchema { c := *n; c.max = &v; return &c }

func (n *numberSchema) run(v any) (any, []Issue) {
	if n.coerce {
		v = ToNumber(v)
	}
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return v, invalidType("number", v)
	}
	if n.isInt {
		if f != math.Trunc(f) {
			return v, issue("invalid_type", "Invalid input: expected int, received number")
		}
		if math.Abs(f) > 9007199254740991 {
			if f > 0 {
				return v, issue("too_big", "Too big: expected int to be <=9007199254740991")
			}
			return v, issue("too_small", "Too small: expected int to be >=-9007199254740991")
		}
	}
	if n.min != nil && f < *n.min {
		return v, issue("too_small", "Too small: expected number to be >="+jsonv.FormatNumber(*n.min))
	}
	if n.max != nil && f > *n.max {
		return v, issue("too_big", "Too big: expected number to be <="+jsonv.FormatNumber(*n.max))
	}
	return f, nil
}

// ToNumber implements JavaScript's Number(value) for JSON values.
func ToNumber(v any) any {
	switch t := v.(type) {
	case nil:
		return 0.0
	case bool:
		if t {
			return 1.0
		}
		return 0.0
	case float64:
		return t
	case string:
		return stringToNumber(t)
	case []any:
		return stringToNumber(arrayToString(t))
	case *jsonv.Object:
		return math.NaN()
	}
	if jsonv.IsUndefined(v) {
		return math.NaN()
	}
	return math.NaN()
}

func arrayToString(a []any) string {
	parts := make([]string, len(a))
	for i, x := range a {
		switch t := x.(type) {
		case nil:
			parts[i] = ""
		case string:
			parts[i] = t
		case bool:
			parts[i] = strconv.FormatBool(t)
		case float64:
			parts[i] = jsonv.FormatNumber(t)
			if math.IsNaN(t) {
				parts[i] = "NaN"
			} else if math.IsInf(t, 1) {
				parts[i] = "Infinity"
			} else if math.IsInf(t, -1) {
				parts[i] = "-Infinity"
			}
		case []any:
			parts[i] = arrayToString(t)
		default:
			parts[i] = "[object Object]"
		}
	}
	return strings.Join(parts, ",")
}

func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func stringToNumber(s string) float64 {
	s = strings.TrimFunc(s, isJSWhitespace)
	if s == "" {
		return 0
	}
	if len(s) > 2 && s[0] == '0' {
		radix := 0
		switch s[1] {
		case 'x', 'X':
			radix = 16
		case 'o', 'O':
			radix = 8
		case 'b', 'B':
			radix = 2
		}
		if radix != 0 {
			digits := s[2:]
			var f float64
			for _, r := range digits {
				d, err := strconv.ParseUint(string(r), radix, 8)
				if err != nil {
					return math.NaN()
				}
				f = f*float64(radix) + float64(d)
			}
			return f
		}
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	// StrDecimalLiteral: [+-] digits [. digits] [e[+-]digits] | [+-] . digits [...]
	body := s
	if body[0] == '+' || body[0] == '-' {
		body = body[1:]
	}
	if body == "" || strings.ContainsAny(body, "xXinfINF_") {
		return math.NaN()
	}
	seenDigit, seenDot, seenExp := false, false, false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= '0' && c <= '9':
			seenDigit = true
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
		case (c == 'e' || c == 'E') && seenDigit && !seenExp:
			seenExp = true
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
	if !seenDigit {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		var ne *strconv.NumError
		if ok := asNumErr(err, &ne); ok && ne.Err == strconv.ErrRange {
			return f
		}
		return math.NaN()
	}
	return f
}

func asNumErr(err error, target **strconv.NumError) bool {
	ne, ok := err.(*strconv.NumError)
	if ok {
		*target = ne
	}
	return ok
}

type boolSchema struct{ base }

// Boolean is z.boolean().
func Boolean() Schema { return boolSchema{} }

func (boolSchema) run(v any) (any, []Issue) {
	if _, ok := v.(bool); !ok {
		return v, invalidType("boolean", v)
	}
	return v, nil
}

type nullSchema struct{ base }

// Null is z.null().
func Null() Schema { return nullSchema{} }

func (nullSchema) run(v any) (any, []Issue) {
	if v != nil {
		return v, invalidType("null", v)
	}
	return nil, nil
}

type literalSchema struct {
	base
	value any
}

// Literal is z.literal(value) for a string, number or boolean.
func Literal(value any) Schema { return literalSchema{value: value} }

func (l literalSchema) run(v any) (any, []Issue) {
	if v == l.value {
		return v, nil
	}
	return v, issue("invalid_value", fmt.Sprintf("Invalid input: expected %s", jsonv.MustStringify(l.value)))
}

type enumSchema struct {
	base
	values []string
}

// Enum is z.enum([...]).
func Enum(values ...string) Schema { return enumSchema{values: values} }

func (e enumSchema) run(v any) (any, []Issue) {
	if s, ok := v.(string); ok {
		for _, x := range e.values {
			if x == s {
				return v, nil
			}
		}
	}
	return v, issue("invalid_value", "Invalid option: expected one of "+strings.Join(e.values, "|"))
}

type unknownSchema struct{ base }

// Unknown is z.unknown() (and z.any()).
func Unknown() Schema { return unknownSchema{base{"optional", "optional"}} }

func (unknownSchema) run(v any) (any, []Issue) { return v, nil }

type neverSchema struct{ base }

func (neverSchema) run(v any) (any, []Issue) { return v, issue("invalid_type", "Invalid input") }

// ---- containers ----

type arraySchema struct {
	base
	elem Schema
}

// Array is z.array(elem).
func Array(elem Schema) Schema { return arraySchema{elem: elem} }

func (a arraySchema) run(v any) (any, []Issue) {
	arr, ok := v.([]any)
	if !ok {
		return v, invalidType("array", v)
	}
	out := make([]any, len(arr))
	var all []Issue
	for i, x := range arr {
		r, issues := a.elem.run(x)
		if len(issues) > 0 {
			all = append(all, prefix(float64(i), issues)...)
		}
		if jsonv.IsUndefined(r) {
			r = nil
		}
		out[i] = r
	}
	return out, all
}

type recordSchema struct {
	base
	value Schema
}

// Record is z.record(z.string(), value).
func Record(value Schema) Schema { return recordSchema{value: value} }

func (r recordSchema) run(v any) (any, []Issue) {
	obj, ok := v.(*jsonv.Object)
	if !ok || obj == nil {
		return v, invalidType("record", v)
	}
	out := jsonv.NewObject()
	var all []Issue
	for _, k := range obj.Keys() {
		x, _ := obj.Get(k)
		res, issues := r.value.run(x)
		if len(issues) > 0 {
			all = append(all, prefix(k, issues)...)
			continue
		}
		out.Set(k, res)
	}
	return out, all
}

// Field is one declared object key.
type Field struct {
	Key    string
	Schema Schema
}

// F builds a Field.
func F(key string, s Schema) Field { return Field{key, s} }

type objectMode int

const (
	strip objectMode = iota
	loose
	strict
	catchall
)

// ObjectSchema is z.object / z.looseObject / z.strictObject.
type ObjectSchema struct {
	base
	fields []Field
	mode   objectMode
	rest   Schema
}

// Object is z.object(shape) (strips undeclared keys).
func Object(fields ...Field) *ObjectSchema { return &ObjectSchema{fields: fields} }

// LooseObject is z.looseObject(shape) (keeps undeclared keys).
func LooseObject(fields ...Field) *ObjectSchema {
	return &ObjectSchema{fields: fields, mode: loose, rest: Unknown()}
}

// Loose returns a copy that keeps undeclared keys (`.loose()`).
func (o *ObjectSchema) Loose() *ObjectSchema {
	c := *o
	c.mode, c.rest = loose, Unknown()
	return &c
}

// Strict returns a copy that rejects undeclared keys (`.strict()`).
func (o *ObjectSchema) Strict() *ObjectSchema {
	c := *o
	c.mode, c.rest = strict, nil
	return &c
}

// Catchall returns a copy that parses undeclared keys with s (`.catchall(s)`).
func (o *ObjectSchema) Catchall(s Schema) *ObjectSchema {
	c := *o
	c.mode, c.rest = catchall, s
	return &c
}

// Extend returns a copy with fields added or replaced (`.extend()`); a replaced key keeps its
// original position, as with an object spread.
func (o *ObjectSchema) Extend(fields ...Field) *ObjectSchema {
	c := *o
	c.fields = append([]Field(nil), o.fields...)
	for _, f := range fields {
		replaced := false
		for i := range c.fields {
			if c.fields[i].Key == f.Key {
				c.fields[i] = f
				replaced = true
				break
			}
		}
		if !replaced {
			c.fields = append(c.fields, f)
		}
	}
	return &c
}

// Fields returns the declared fields.
func (o *ObjectSchema) Fields() []Field { return append([]Field(nil), o.fields...) }

func (o *ObjectSchema) run(v any) (any, []Issue) {
	in, ok := v.(*jsonv.Object)
	if !ok || in == nil {
		return v, invalidType("object", v)
	}
	out := jsonv.NewObject()
	var all []Issue
	declared := map[string]bool{}
	for _, f := range o.fields {
		declared[f.Key] = true
		if f.Key == "__proto__" {
			continue
		}
		x, present := in.Get(f.Key)
		if !present {
			x = Undefined
		}
		res, issues := f.Schema.run(x)
		optin, optout := f.Schema.opt()
		all = append(all, handleProperty(out, f.Key, present, res, issues, optin, optout)...)
	}
	switch o.mode {
	case strip:
	case strict:
		var unknown []string
		for _, k := range in.Keys() {
			if !declared[k] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			quoted := make([]string, len(unknown))
			for i, k := range unknown {
				quoted[i] = strconv.Quote(k)
			}
			noun := "key"
			if len(unknown) > 1 {
				noun = "keys"
			}
			all = append(all, Issue{Code: "unrecognized_keys", Path: []any{}, Message: "Unrecognized " + noun + ": " + strings.Join(quoted, ", ")})
		}
	default:
		for _, k := range in.Keys() {
			if declared[k] || k == "__proto__" {
				continue
			}
			x, _ := in.Get(k)
			res, issues := o.rest.run(x)
			optin, optout := o.rest.opt()
			all = append(all, handleProperty(out, k, true, res, issues, optin, optout)...)
		}
	}
	return out, all
}

func handleProperty(out *jsonv.Object, key string, present bool, res any, issues []Issue, optin, optout string) []Issue {
	optionalOut := optout == "optional"
	if !present && optionalOut && optin == "optional" {
		return nil
	}
	var all []Issue
	if len(issues) > 0 {
		if optin != "" && optionalOut && !present {
			return nil
		}
		all = append(all, prefix(key, issues)...)
	}
	if !present && optin == "" {
		if len(issues) == 0 {
			all = append(all, Issue{Code: "invalid_type", Path: []any{key}, Message: "Invalid input: expected nonoptional, received undefined"})
		}
		return all
	}
	if jsonv.IsUndefined(res) {
		if present || (optin == "defaulted" && !optionalOut) {
			out.Set(key, Undefined)
		}
	} else {
		out.Set(key, res)
	}
	return all
}

// ---- wrappers ----

type optionalSchema struct{ inner Schema }

// Optional is `.optional()`.
func Optional(s Schema) Schema { return optionalSchema{s} }

func (o optionalSchema) opt() (string, string) { return "optional", "optional" }
func (o optionalSchema) run(v any) (any, []Issue) {
	if jsonv.IsUndefined(v) {
		return Undefined, nil
	}
	return o.inner.run(v)
}

type defaultSchema struct {
	inner Schema
	value func() any
}

// Default is `.default(value)`; value is called for each absent input.
func Default(s Schema, value func() any) Schema { return defaultSchema{s, value} }

func (d defaultSchema) opt() (string, string) {
	_, out := d.inner.opt()
	return "defaulted", out
}
func (d defaultSchema) run(v any) (any, []Issue) {
	if jsonv.IsUndefined(v) {
		return d.value(), nil
	}
	return d.inner.run(v)
}

type catchSchema struct {
	inner Schema
	value any
}

// Catch is `.catch(value)`; pass Undefined for `.catch(undefined)`.
func Catch(s Schema, value any) Schema { return catchSchema{s, value} }

func (c catchSchema) opt() (string, string) { return c.inner.opt() }
func (c catchSchema) run(v any) (any, []Issue) {
	out, issues := c.inner.run(v)
	if len(issues) > 0 {
		return c.value, nil
	}
	return out, nil
}

type unionSchema struct {
	base
	options []Schema
}

// Union is z.union([...]): the first option that parses wins.
func Union(options ...Schema) Schema {
	in, out := "", ""
	for _, o := range options {
		i, x := o.opt()
		if i == "optional" {
			in = "optional"
		}
		if x == "optional" {
			out = "optional"
		}
	}
	return unionSchema{base{in, out}, options}
}

func (u unionSchema) run(v any) (any, []Issue) {
	for _, o := range u.options {
		res, issues := o.run(v)
		if len(issues) == 0 {
			return res, nil
		}
	}
	return v, issue("invalid_union", "Invalid input")
}

type transformSchema struct {
	inner Schema
	fn    func(any) (any, error)
}

// Transform is `.transform(fn)`.
func Transform(s Schema, fn func(any) (any, error)) Schema { return transformSchema{s, fn} }

func (t transformSchema) opt() (string, string) { return t.inner.opt() }
func (t transformSchema) run(v any) (any, []Issue) {
	out, issues := t.inner.run(v)
	if len(issues) > 0 {
		return out, issues
	}
	res, err := t.fn(out)
	if err != nil {
		return out, issue("custom", err.Error())
	}
	return res, nil
}

type refineSchema struct {
	inner Schema
	check func(any) []Issue
}

// SuperRefine adds a check that may report issues (`.superRefine()`/`.refine()`).
func SuperRefine(s Schema, check func(any) []Issue) Schema { return refineSchema{s, check} }

// Refine adds a predicate check with a message.
func Refine(s Schema, pred func(any) bool, msg string) Schema {
	return refineSchema{s, func(v any) []Issue {
		if pred(v) {
			return nil
		}
		return issue("custom", msg)
	}}
}

// CustomIssue builds an issue list for SuperRefine.
func CustomIssue(msg string) []Issue { return issue("custom", msg) }

func (r refineSchema) opt() (string, string) { return r.inner.opt() }
func (r refineSchema) run(v any) (any, []Issue) {
	out, issues := r.inner.run(v)
	if len(issues) > 0 {
		return out, issues
	}
	if extra := r.check(out); len(extra) > 0 {
		return out, extra
	}
	return out, nil
}

type pipeSchema struct{ a, b Schema }

// Pipe is `.pipe(b)`.
func Pipe(a, b Schema) Schema { return pipeSchema{a, b} }

func (p pipeSchema) opt() (string, string) {
	in, _ := p.a.opt()
	_, out := p.b.opt()
	return in, out
}
func (p pipeSchema) run(v any) (any, []Issue) {
	out, issues := p.a.run(v)
	if len(issues) > 0 {
		return out, issues
	}
	return p.b.run(out)
}

// Preprocess is z.preprocess(fn, s).
func Preprocess(fn func(any) any, s Schema) Schema {
	return Pipe(Transform(Unknown(), func(v any) (any, error) { return fn(v), nil }), s)
}

type lazySchema struct {
	fn func() Schema
}

// Lazy is z.lazy(fn).
func Lazy(fn func() Schema) Schema { return lazySchema{fn} }

// opt is fixed: lazy schemas here wrap records/unions, which are never optional, and evaluating
// fn during package initialization would observe uninitialized schemas.
func (l lazySchema) opt() (string, string)    { return "", "" }
func (l lazySchema) run(v any) (any, []Issue) { return l.fn().run(v) }

type intersectionSchema struct {
	base
	a, b Schema
}

// Intersection is z.intersection(a, b): both must parse; object results are merged.
func Intersection(a, b Schema) Schema {
	ai, ao := a.opt()
	bi, bo := b.opt()
	in, out := "", ""
	if ai == "optional" && bi == "optional" {
		in = "optional"
	}
	if ao == "optional" && bo == "optional" {
		out = "optional"
	}
	return intersectionSchema{base{in, out}, a, b}
}

func (s intersectionSchema) run(v any) (any, []Issue) {
	ra, ia := s.a.run(v)
	rb, ib := s.b.run(v)
	if len(ia)+len(ib) > 0 {
		return v, append(ia, ib...)
	}
	merged, ok := mergeValues(ra, rb)
	if !ok {
		return v, issue("invalid_intersection", "Intersection results could not be merged")
	}
	return merged, nil
}

func mergeValues(a, b any) (any, bool) {
	if jsonv.Equal(a, b) {
		return a, true
	}
	oa, okA := a.(*jsonv.Object)
	ob, okB := b.(*jsonv.Object)
	if okA && okB {
		out := oa.Clone()
		for _, k := range ob.Keys() {
			bv, _ := ob.Get(k)
			if av, shared := oa.Get(k); shared {
				m, ok := mergeValues(av, bv)
				if !ok {
					return nil, false
				}
				out.Set(k, m)
			} else {
				out.Set(k, bv)
			}
		}
		return out, true
	}
	if jsonv.IsUndefined(a) {
		return b, true
	}
	if jsonv.IsUndefined(b) {
		return a, true
	}
	return nil, false
}
