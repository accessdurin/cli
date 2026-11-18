// Package zod reproduces, over jsjson values, the subset of zod 4.1.13 that the CLI's schemas
// use. It matters because the TypeScript CLI printed, hashed and persisted zod's outputs, and
// branched on which issues zod reported:
//
//   - z.object strips unknown keys and writes its output keys in schema order; .strict()
//     reports unknown keys; .catchall(s) keeps them, after the schema's own keys.
//   - z.record and z.json drop a "__proto__" key, as zod does.
//   - z.url() and other string formats output the input trimmed (z.url) or unchanged.
//   - .default(v) returns v for undefined without validating it.
//   - Checks (refine, min, regex, ...) run after their schema's own parse only while no
//     earlier issue aborted the value. A type mismatch, a missing key, an unknown key or an
//     enum/literal mismatch aborts; a failed check or string format does not, so a later
//     refine still sees the (type-correct) value. Profiles depend on that: a host problem is
//     reported even when another field fails a format check.
//   - A transform runs only when its input had no issue at all.
//   - A union returns the first option without issues; failing that, the only option whose
//     issues did not abort; failing that, an invalid_union issue.
//
// A nil jsjson.Value is JavaScript's undefined. Messages exist only for custom issues, which
// is all the CLI ever shows.
package zod

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
)

// Issue is one zod issue. Path elements are strings (keys) or ints (indices).
type Issue struct {
	Code     string
	Path     []any
	Message  string
	Continue bool
}

// Error is a ZodError: an unexpected failure as far as the CLI's error printing goes.
type Error struct{ Issues []Issue }

func (e *Error) Error() string {
	if len(e.Issues) == 0 {
		return "invalid input"
	}
	i := e.Issues[0]
	return fmt.Sprintf("invalid input: %s at %v", i.Code, i.Path)
}

// Payload is a value being parsed and the issues found so far.
type Payload struct {
	Value   jsjson.Value
	Issues  []Issue
	aborted bool
}

func (p *Payload) issue(code string, cont bool) {
	p.Issues = append(p.Issues, Issue{Code: code, Continue: cont})
}

// abortedFrom is util.aborted(payload, start).
func (p *Payload) abortedFrom(start int) bool {
	if p.aborted {
		return true
	}
	for _, i := range p.Issues[start:] {
		if !i.Continue {
			return true
		}
	}
	return false
}

// Schema is a zod schema.
type Schema interface {
	Run(p *Payload)
}

// SafeParse is schema.safeParse(value).
func SafeParse(s Schema, v jsjson.Value) (jsjson.Value, *Error) {
	p := &Payload{Value: v}
	s.Run(p)
	if len(p.Issues) > 0 {
		return nil, &Error{Issues: p.Issues}
	}
	return p.Value, nil
}

// Parse is schema.parse(value): the error is a *Error.
func Parse(s Schema, v jsjson.Value) (jsjson.Value, error) {
	out, err := SafeParse(s, v)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Valid reports whether schema.safeParse(value).success.
func Valid(s Schema, v jsjson.Value) bool {
	_, err := SafeParse(s, v)
	return err == nil
}

// --- checks ------------------------------------------------------------------------------

// Check is one zod check: it inspects (or, for overwrites such as trim, replaces) p.Value
// and appends issues.
type Check func(p *Payload)

// runChecks is $ZodType's runChecks: a check is skipped once an issue aborted the value.
func runChecks(p *Payload, checks []Check) {
	aborted := p.abortedFrom(0)
	for _, ch := range checks {
		if aborted {
			continue
		}
		n := len(p.Issues)
		ch(p)
		if len(p.Issues) > n {
			aborted = p.abortedFrom(n)
		}
	}
}

// Custom is a refine's check: fn sees the parsed value; a failure is a non-aborting custom
// issue with message at path.
func Custom(fn func(v jsjson.Value) bool, message string, path ...any) Check {
	return func(p *Payload) {
		if !fn(p.Value) {
			p.Issues = append(p.Issues, Issue{Code: "custom", Message: message, Path: append([]any(nil), path...), Continue: true})
		}
	}
}

// Context lets a superRefine add issues.
type Context struct{ p *Payload }

// AddIssue adds a custom, non-aborting issue.
func (c Context) AddIssue(message string, path ...any) {
	c.p.Issues = append(c.p.Issues, Issue{Code: "custom", Message: message, Path: append([]any(nil), path...), Continue: true})
}

type checked struct {
	inner  Schema
	checks []Check
}

func (c *checked) Run(p *Payload) {
	c.inner.Run(p)
	runChecks(p, c.checks)
}

// With returns s with extra checks, like chaining .refine()/.check() on any schema.
func With(s Schema, checks ...Check) Schema {
	if o, ok := s.(*ObjectSchema); ok {
		return o.With(checks...)
	}
	return &checked{inner: s, checks: checks}
}

// Refine is s.refine(fn, message).
func Refine(s Schema, fn func(v jsjson.Value) bool, message string, path ...any) Schema {
	return With(s, Custom(fn, message, path...))
}

// SuperRefine is s.superRefine(fn).
func SuperRefine(s Schema, fn func(v jsjson.Value, ctx Context)) Schema {
	return With(s, func(p *Payload) { fn(p.Value, Context{p}) })
}

type pipe struct {
	in Schema
	fn func(jsjson.Value) jsjson.Value
}

func (t *pipe) Run(p *Payload) {
	t.in.Run(p)
	if len(p.Issues) > 0 {
		p.aborted = true
		return
	}
	p.Value = t.fn(p.Value)
}

// Transform is s.transform(fn): fn runs only when s reported no issue.
func Transform(s Schema, fn func(jsjson.Value) jsjson.Value) Schema { return &pipe{in: s, fn: fn} }

// --- strings -----------------------------------------------------------------------------

// StringSchema is z.string() with its checks.
type StringSchema struct{ checks []Check }

// String is z.string().
func String() *StringSchema { return &StringSchema{} }

func (s *StringSchema) Run(p *Payload) {
	if _, ok := p.Value.(jsjson.String); !ok {
		p.issue("invalid_type", false)
		return
	}
	runChecks(p, s.checks)
}

func (s *StringSchema) with(c Check) *StringSchema {
	return &StringSchema{checks: append(append([]Check(nil), s.checks...), c)}
}

func str(p *Payload) string { return string(p.Value.(jsjson.String)) }

// Min is .min(n), in UTF-16 code units like String#length.
func (s *StringSchema) Min(n int) *StringSchema {
	return s.with(func(p *Payload) {
		if jsjson.UTF16Len(str(p)) < n {
			p.issue("too_small", true)
		}
	})
}

// Max is .max(n), in UTF-16 code units.
func (s *StringSchema) Max(n int) *StringSchema {
	return s.with(func(p *Payload) {
		if jsjson.UTF16Len(str(p)) > n {
			p.issue("too_big", true)
		}
	})
}

// Regex is .regex(re). re must be written to behave on the CLI's inputs as the JavaScript
// pattern does (anchored ASCII patterns do).
func (s *StringSchema) Regex(re *regexp.Regexp) *StringSchema {
	return s.with(func(p *Payload) {
		if !re.MatchString(str(p)) {
			p.issue("invalid_format", true)
		}
	})
}

// Refine is .refine(fn) on a string.
func (s *StringSchema) Refine(fn func(string) bool, message string) *StringSchema {
	return s.with(func(p *Payload) {
		if !fn(str(p)) {
			p.Issues = append(p.Issues, Issue{Code: "custom", Message: message, Continue: true})
		}
	})
}

// Trim is .trim(): the value becomes String#trim() of itself.
func (s *StringSchema) Trim() *StringSchema {
	return s.with(func(p *Payload) { p.Value = jsjson.String(TrimJS(str(p))) })
}

// Format adds a string format check (z.url(), z.uuid(), ...): valid reports acceptance and
// may return the output value.
func (s *StringSchema) Format(valid func(string) (string, bool)) *StringSchema {
	return s.with(func(p *Payload) {
		out, ok := valid(str(p))
		if !ok {
			p.issue("invalid_format", true)
			return
		}
		p.Value = jsjson.String(out)
	})
}

var (
	uuidPattern     = regexp.MustCompile(`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}|00000000-0000-0000-0000-000000000000|ffffffff-ffff-ffff-ffff-ffffffffffff)$`)
	dateSource      = `(?:(?:\d\d[2468][048]|\d\d[13579][26]|\d\d0[48]|[02468][048]00|[13579][26]00)-02-29|\d{4}-(?:(?:0[13578]|1[02])-(?:0[1-9]|[12]\d|3[01])|(?:0[469]|11)-(?:0[1-9]|[12]\d|30)|(?:02)-(?:0[1-9]|1\d|2[0-8])))`
	datetimePattern = regexp.MustCompile(`^` + dateSource + `T(?:(?:[01]\d|2[0-3]):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?(?:Z))$`)
)

// IsUUID is z.uuid()'s pattern (any RFC 9562 version, nil or max).
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// UUID is z.uuid().
func UUID() *StringSchema {
	return String().Format(func(s string) (string, bool) { return s, IsUUID(s) })
}

// ISODateTime is z.iso.datetime(): UTC "Z" only, optional seconds and fraction.
func ISODateTime() *StringSchema {
	return String().Format(func(s string) (string, bool) { return s, datetimePattern.MatchString(s) })
}

// TrimJS is String.prototype.trim.
func TrimJS(s string) string { return strings.TrimFunc(s, IsJSWhiteSpace) }

// IsJSWhiteSpace is ECMAScript WhiteSpace or LineTerminator.
func IsJSWhiteSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f,
		0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// --- numbers, booleans, literals -----------------------------------------------------------

// NumberSchema is z.number() with its checks.
type NumberSchema struct{ checks []Check }

// Number is z.number(): any finite number.
func Number() *NumberSchema { return &NumberSchema{} }

func (s *NumberSchema) Run(p *Payload) {
	if _, ok := p.Value.(jsjson.Number); !ok {
		p.issue("invalid_type", false)
		return
	}
	runChecks(p, s.checks)
}

func (s *NumberSchema) with(c Check) *NumberSchema {
	return &NumberSchema{checks: append(append([]Check(nil), s.checks...), c)}
}

func num(p *Payload) float64 { return float64(p.Value.(jsjson.Number)) }

const maxSafeInteger = 9007199254740991

// Int is .int(): a safe integer. A non-integer aborts, as in zod.
func (s *NumberSchema) Int() *NumberSchema {
	return s.with(func(p *Payload) {
		n := num(p)
		if math.Trunc(n) != n {
			p.issue("invalid_type", false)
			return
		}
		if n > maxSafeInteger || n < -maxSafeInteger {
			p.issue("too_big", true)
		}
	})
}

func (s *NumberSchema) compare(ok func(float64) bool, code string) *NumberSchema {
	return s.with(func(p *Payload) {
		if !ok(num(p)) {
			p.issue(code, true)
		}
	})
}

// Min is .min(n) (>=).
func (s *NumberSchema) Min(n float64) *NumberSchema {
	return s.compare(func(x float64) bool { return x >= n }, "too_small")
}

// Max is .max(n) (<=).
func (s *NumberSchema) Max(n float64) *NumberSchema {
	return s.compare(func(x float64) bool { return x <= n }, "too_big")
}

// Positive is .positive() (> 0).
func (s *NumberSchema) Positive() *NumberSchema {
	return s.compare(func(x float64) bool { return x > 0 }, "too_small")
}

// Nonnegative is .nonnegative() (>= 0).
func (s *NumberSchema) Nonnegative() *NumberSchema { return s.Min(0) }

type simple func(v jsjson.Value) bool

func (s simple) Run(p *Payload) {
	if !s(p.Value) {
		p.issue("invalid_type", false)
	}
}

// Boolean is z.boolean().
func Boolean() Schema {
	return simple(func(v jsjson.Value) bool { _, ok := v.(jsjson.Bool); return ok })
}

// Null is z.null().
func Null() Schema {
	return simple(func(v jsjson.Value) bool { _, ok := v.(jsjson.Null); return ok })
}

// Never is z.never().
func Never() Schema { return simple(func(jsjson.Value) bool { return false }) }

// Unknown is z.unknown().
func Unknown() Schema { return simple(func(jsjson.Value) bool { return true }) }

// LiteralSchema is z.literal(value) for a string, number or boolean.
type LiteralSchema struct{ Value jsjson.Value }

// Literal is z.literal(v).
func Literal(v jsjson.Value) *LiteralSchema { return &LiteralSchema{Value: v} }

func (l *LiteralSchema) Run(p *Payload) {
	if !strictEqual(p.Value, l.Value) {
		p.issue("invalid_value", false)
	}
}

func strictEqual(a, b jsjson.Value) bool {
	switch x := a.(type) {
	case jsjson.String, jsjson.Bool, jsjson.Null:
		return a == b
	case jsjson.Number:
		y, ok := b.(jsjson.Number)
		return ok && x == y
	}
	return false
}

// Enum is z.enum(values) of strings.
func Enum(values ...string) Schema {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	return simple(func(v jsjson.Value) bool {
		s, ok := v.(jsjson.String)
		return ok && set[string(s)]
	})
}

// --- wrappers ------------------------------------------------------------------------------

type optional struct{ inner Schema }

func (o optional) Run(p *Payload) {
	if p.Value == nil {
		return
	}
	o.inner.Run(p)
}

// Optional is s.optional().
func Optional(s Schema) Schema { return optional{s} }

type nullable struct{ inner Schema }

func (n nullable) Run(p *Payload) {
	if _, ok := p.Value.(jsjson.Null); ok {
		return
	}
	n.inner.Run(p)
}

// Nullable is s.nullable().
func Nullable(s Schema) Schema { return nullable{s} }

// Nullish is s.nullish().
func Nullish(s Schema) Schema { return Optional(Nullable(s)) }

type withDefault struct {
	inner Schema
	value func() jsjson.Value
}

func (d withDefault) Run(p *Payload) {
	if p.Value == nil {
		p.Value = d.value()
		return
	}
	d.inner.Run(p)
	if p.Value == nil {
		p.Value = d.value()
	}
}

// Default is s.default(v): v (made fresh by value) replaces undefined unvalidated.
func Default(s Schema, value func() jsjson.Value) Schema { return withDefault{s, value} }

// --- containers ----------------------------------------------------------------------------

func prefix(key any, issues []Issue) []Issue {
	out := make([]Issue, len(issues))
	for i, is := range issues {
		is.Path = append([]any{key}, is.Path...)
		out[i] = is
	}
	return out
}

// ArraySchema is z.array(element) with its checks.
type ArraySchema struct {
	element Schema
	checks  []Check
}

// Array is z.array(element).
func Array(element Schema) *ArraySchema { return &ArraySchema{element: element} }

func (a *ArraySchema) Run(p *Payload) {
	in, ok := p.Value.(jsjson.Array)
	if !ok {
		p.issue("invalid_type", false)
		return
	}
	out := make(jsjson.Array, len(in))
	for i, item := range in {
		sub := &Payload{Value: item}
		a.element.Run(sub)
		p.Issues = append(p.Issues, prefix(i, sub.Issues)...)
		out[i] = sub.Value
	}
	p.Value = out
	runChecks(p, a.checks)
}

// Max is .max(n) elements.
func (a *ArraySchema) Max(n int) *ArraySchema {
	return &ArraySchema{element: a.element, checks: append(append([]Check(nil), a.checks...), func(p *Payload) {
		if len(p.Value.(jsjson.Array)) > n {
			p.issue("too_big", true)
		}
	})}
}

type record struct{ value Schema }

// Record is z.record(z.string(), value): any non-array object; "__proto__" is dropped.
func Record(value Schema) Schema { return record{value} }

func (r record) Run(p *Payload) {
	in, ok := p.Value.(*jsjson.Object)
	if !ok || in == nil {
		p.issue("invalid_type", false)
		return
	}
	out := jsjson.NewObject()
	for _, m := range in.Members() {
		if m.Key == "__proto__" {
			continue
		}
		sub := &Payload{Value: m.Value}
		r.value.Run(sub)
		p.Issues = append(p.Issues, prefix(m.Key, sub.Issues)...)
		out.Set(m.Key, sub.Value)
	}
	p.Value = out
}

// Field is one key of an object shape.
type Field struct {
	Key    string
	Schema Schema
}

// F is shorthand for a Field.
func F(key string, s Schema) Field { return Field{key, s} }

// ObjectSchema is z.object(shape) with its unknown-key mode and checks.
type ObjectSchema struct {
	fields   []Field
	strict   bool
	catchall Schema
	checks   []Check
}

// Object is z.object(shape) (strip mode).
func Object(fields ...Field) *ObjectSchema { return &ObjectSchema{fields: fields} }

func (o *ObjectSchema) clone() *ObjectSchema {
	c := *o
	c.fields = append([]Field(nil), o.fields...)
	c.checks = append([]Check(nil), o.checks...)
	return &c
}

// Strict is .strict().
func (o *ObjectSchema) Strict() *ObjectSchema {
	c := o.clone()
	c.strict, c.catchall = true, nil
	return c
}

// Catchall is .catchall(s).
func (o *ObjectSchema) Catchall(s Schema) *ObjectSchema {
	c := o.clone()
	c.strict, c.catchall = false, s
	return c
}

// Extend is .extend(shape) / .safeExtend(shape): replaced keys keep their position, new keys
// follow; the mode and checks are kept.
func (o *ObjectSchema) Extend(fields ...Field) *ObjectSchema {
	c := o.clone()
	for _, f := range fields {
		replaced := false
		for i := range c.fields {
			if c.fields[i].Key == f.Key {
				c.fields[i] = f
				replaced = true
			}
		}
		if !replaced {
			c.fields = append(c.fields, f)
		}
	}
	return c
}

// With returns o with extra checks (refine, superRefine).
func (o *ObjectSchema) With(checks ...Check) *ObjectSchema {
	c := o.clone()
	c.checks = append(c.checks, checks...)
	return c
}

// Refine is .refine(fn, message).
func (o *ObjectSchema) Refine(fn func(*jsjson.Object) bool, message string, path ...any) *ObjectSchema {
	return o.With(Custom(func(v jsjson.Value) bool { return fn(v.(*jsjson.Object)) }, message, path...))
}

// SuperRefine is .superRefine(fn).
func (o *ObjectSchema) SuperRefine(fn func(*jsjson.Object, Context)) *ObjectSchema {
	return o.With(func(p *Payload) { fn(p.Value.(*jsjson.Object), Context{p}) })
}

// Shape returns the schema of key, or nil.
func (o *ObjectSchema) Shape(key string) Schema {
	for _, f := range o.fields {
		if f.Key == key {
			return f.Schema
		}
	}
	return nil
}

func (o *ObjectSchema) Run(p *Payload) {
	in, ok := p.Value.(*jsjson.Object)
	if !ok || in == nil {
		p.issue("invalid_type", false)
		return
	}
	out := jsjson.NewObject()
	known := map[string]bool{}
	for _, f := range o.fields {
		known[f.Key] = true
		v, present := in.Get(f.Key)
		sub := &Payload{Value: v}
		f.Schema.Run(sub)
		p.Issues = append(p.Issues, prefix(f.Key, sub.Issues)...)
		if sub.Value != nil || present {
			out.Set(f.Key, sub.Value)
		}
	}
	if o.strict || o.catchall != nil {
		var unknown []string
		for _, m := range in.Members() {
			if known[m.Key] {
				continue
			}
			if o.strict {
				unknown = append(unknown, m.Key)
				continue
			}
			sub := &Payload{Value: m.Value}
			o.catchall.Run(sub)
			p.Issues = append(p.Issues, prefix(m.Key, sub.Issues)...)
			// Assigning "__proto__" on a plain object sets its prototype instead of a key.
			if m.Key != "__proto__" {
				out.Set(m.Key, sub.Value)
			}
		}
		if len(unknown) > 0 {
			p.issue("unrecognized_keys", false)
		}
	}
	p.Value = out
	runChecks(p, o.checks)
}

type union struct{ options []Schema }

// Union is z.union(options).
func Union(options ...Schema) Schema { return union{options} }

func (u union) Run(p *Payload) {
	if len(u.options) == 1 {
		u.options[0].Run(p)
		return
	}
	results := make([]*Payload, 0, len(u.options))
	for _, o := range u.options {
		r := &Payload{Value: p.Value}
		o.Run(r)
		if len(r.Issues) == 0 {
			p.Value = r.Value
			return
		}
		results = append(results, r)
	}
	var nonaborted []*Payload
	for _, r := range results {
		if !r.abortedFrom(0) {
			nonaborted = append(nonaborted, r)
		}
	}
	if len(nonaborted) == 1 {
		*p = *nonaborted[0]
		return
	}
	p.issue("invalid_union", false)
}

type discriminated struct {
	key     string
	options []*ObjectSchema
}

// DiscriminatedUnion is z.discriminatedUnion(key, options); each option's key must be a
// literal.
func DiscriminatedUnion(key string, options ...*ObjectSchema) Schema {
	return discriminated{key, options}
}

func (d discriminated) Run(p *Payload) {
	in, ok := p.Value.(*jsjson.Object)
	if !ok || in == nil {
		p.issue("invalid_type", false)
		return
	}
	v, _ := in.Get(d.key)
	for _, o := range d.options {
		if lit, ok := o.Shape(d.key).(*LiteralSchema); ok && strictEqual(v, lit.Value) {
			o.Run(p)
			return
		}
	}
	p.Issues = append(p.Issues, Issue{Code: "invalid_union", Path: []any{d.key}})
}

type jsonSchema struct{}

// JSON is z.json(): any JSON value, copied with every object's "__proto__" key dropped.
func JSON() Schema { return jsonSchema{} }

func (jsonSchema) Run(p *Payload) {
	out, ok := jsonCopy(p.Value)
	if !ok {
		p.issue("invalid_union", false)
		return
	}
	p.Value = out
}

func jsonCopy(v jsjson.Value) (jsjson.Value, bool) {
	switch x := v.(type) {
	case jsjson.String, jsjson.Bool, jsjson.Null:
		return v, true
	case jsjson.Number:
		return v, true
	case jsjson.Array:
		out := make(jsjson.Array, len(x))
		for i, e := range x {
			c, ok := jsonCopy(e)
			if !ok {
				return nil, false
			}
			out[i] = c
		}
		return out, true
	case *jsjson.Object:
		if x == nil {
			return nil, false
		}
		out := jsjson.NewObject()
		for _, m := range x.Members() {
			if m.Key == "__proto__" {
				continue
			}
			c, ok := jsonCopy(m.Value)
			if !ok {
				return nil, false
			}
			out.Set(m.Key, c)
		}
		return out, true
	}
	return nil, false
}
