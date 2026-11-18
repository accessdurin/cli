package app

import (
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/jsonc"
)

// fromJSONC converts a jsonc value to the jsjson model.
func fromJSONC(v any) jsjson.Value {
	switch x := v.(type) {
	case nil:
		return jsjson.Null{}
	case bool:
		return jsjson.Bool(x)
	case float64:
		return jsjson.Number(x)
	case string:
		return jsjson.String(x)
	case []any:
		out := make(jsjson.Array, len(x))
		for i, e := range x {
			out[i] = fromJSONC(e)
		}
		return out
	case *jsonc.Object:
		out := jsjson.NewObject()
		for _, k := range x.Keys() {
			e, _ := x.Get(k)
			out.Set(k, fromJSONC(e))
		}
		return out
	}
	return nil
}

// toJSONC converts a jsjson value to the jsonc model, for jsonc.Modify.
func toJSONC(v jsjson.Value) any {
	switch x := v.(type) {
	case jsjson.Null, nil:
		return nil
	case jsjson.Bool:
		return bool(x)
	case jsjson.Number:
		return float64(x)
	case jsjson.String:
		return string(x)
	case jsjson.Array:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = toJSONC(e)
		}
		return out
	case *jsjson.Object:
		out := jsonc.NewObject()
		for _, m := range x.Members() {
			if m.Value != nil {
				out.Set(m.Key, toJSONC(m.Value))
			}
		}
		return out
	}
	return nil
}

// jsoncEdit is applyEdits(raw, modify(raw, path, value, {formattingOptions: {insertSpaces:
// true, tabSize: 2}})).
func jsoncEdit(raw string, path []any, value jsjson.Value) (string, error) {
	edits, err := jsonc.Modify(raw, path, toJSONC(value), jsonc.ModificationOptions{
		FormattingOptions: &jsonc.FormattingOptions{InsertSpaces: true, TabSize: 2},
	})
	if err != nil {
		return "", err
	}
	return jsonc.ApplyEdits(raw, edits)
}
