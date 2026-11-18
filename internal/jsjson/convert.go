package jsjson

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// FromGo converts a Go value to a Value. It understands Value itself, nil
// (null), bool, string, every integer and float kind, json.Number,
// []any, []string, map[string]any and map[string]string (map keys are
// inserted in Go's sorted string order; the object then applies JavaScript's
// key order). Anything else (structs, typed slices and maps, ...) goes
// through encoding/json and is re-parsed, so struct fields keep their
// declaration order. Note that encoding/json replaces each invalid UTF-8
// byte with its own U+FFFD.
func FromGo(x any) (Value, error) {
	switch v := x.(type) {
	case nil:
		return Null{}, nil
	case Value:
		return v, nil
	case bool:
		return Bool(v), nil
	case string:
		return String(v), nil
	case float64:
		return Number(v), nil
	case float32:
		return Number(float64(v)), nil
	case int:
		return Number(float64(v)), nil
	case int8:
		return Number(float64(v)), nil
	case int16:
		return Number(float64(v)), nil
	case int32:
		return Number(float64(v)), nil
	case int64:
		return Number(float64(v)), nil
	case uint:
		return Number(float64(v)), nil
	case uint8:
		return Number(float64(v)), nil
	case uint16:
		return Number(float64(v)), nil
	case uint32:
		return Number(float64(v)), nil
	case uint64:
		return Number(float64(v)), nil
	case json.Number:
		return ParseString(string(v))
	case []string:
		return Strings(v...), nil
	case []any:
		a := make(Array, len(v))
		for i, e := range v {
			c, err := FromGo(e)
			if err != nil {
				return nil, err
			}
			a[i] = c
		}
		return a, nil
	case map[string]string:
		o := &Object{}
		for _, k := range sortedKeys(v) {
			o.Set(k, String(v[k]))
		}
		return o, nil
	case map[string]any:
		o := &Object{}
		for _, k := range sortedKeys(v) {
			c, err := FromGo(v[k])
			if err != nil {
				return nil, err
			}
			o.Set(k, c)
		}
		return o, nil
	}
	if rv := reflect.ValueOf(x); rv.Kind() == reflect.Pointer && rv.IsNil() {
		return Null{}, nil
	}
	raw, err := json.Marshal(x)
	if err != nil {
		return nil, fmt.Errorf("jsjson: cannot convert %T: %w", x, err)
	}
	return Parse(raw)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ToGo converts a Value to encoding/json's generic form: nil, bool, float64,
// string, []any and map[string]any (losing key order). Undefined (nil)
// becomes nil, and extensions are converted through ToJSON.
func ToGo(v Value) any {
	switch x := resolve(v).(type) {
	case Bool:
		return bool(x)
	case Number:
		return float64(x)
	case String:
		return string(x)
	case Array:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = ToGo(e)
		}
		return out
	case *Object:
		out := make(map[string]any, x.Len())
		for _, m := range x.Members() {
			out[m.Key] = ToGo(m.Value)
		}
		return out
	}
	return nil
}

// Decode stores v in dst with encoding/json (via Stringify), for reading a
// parsed value into a typed Go struct. Lone surrogates become U+FFFD and
// non-finite numbers become null on the way.
func Decode(v Value, dst any) error {
	return json.Unmarshal([]byte(Stringify(v)), dst)
}
