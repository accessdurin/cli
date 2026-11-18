package jsonc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The fixtures in testdata are produced by testdata/gen.mjs from the
// upstream npm package jsonc-parser 3.3.1. Regenerate with
// `node internal/jsonc/testdata/gen.mjs`.

type fixtureEdit struct {
	Offset  int    `json:"offset"`
	Length  int    `json:"length"`
	Content string `json:"content"`
}

type fixtureFormatting struct {
	TabSize            int    `json:"tabSize"`
	InsertSpaces       bool   `json:"insertSpaces"`
	EOL                string `json:"eol"`
	InsertFinalNewline bool   `json:"insertFinalNewline"`
	KeepLines          bool   `json:"keepLines"`
}

func (f fixtureFormatting) options() FormattingOptions {
	return FormattingOptions(f)
}

// loadFixture reads a JSON Lines fixture: a header record, then one case
// per line.
func loadFixture[H, C any](t *testing.T, name string) (H, []C) {
	t.Helper()
	var header H
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("%s header: %v", name, err)
	}
	cases := make([]C, len(lines)-1)
	for i, line := range lines[1:] {
		if err := json.Unmarshal([]byte(line), &cases[i]); err != nil {
			t.Fatalf("%s line %d: %v", name, i+2, err)
		}
	}
	if len(cases) == 0 {
		t.Fatalf("%s has no cases", name)
	}
	return header, cases
}

func toEdits(list []fixtureEdit) []Edit {
	out := make([]Edit, len(list))
	for i, e := range list {
		out[i] = Edit(e)
	}
	return out
}

func TestParseFixtures(t *testing.T) {
	type parseCase struct {
		Text    string `json:"text"`
		Options struct {
			DisallowComments   bool `json:"disallowComments"`
			AllowTrailingComma bool `json:"allowTrailingComma"`
			AllowEmptyContent  bool `json:"allowEmptyContent"`
		} `json:"options"`
		Undefined bool    `json:"undefined"`
		Value     *string `json:"value"`
		Errors    []struct {
			Code   int `json:"code"`
			Offset int `json:"offset"`
			Length int `json:"length"`
		} `json:"errors"`
	}
	_, cases := loadFixture[struct{}, parseCase](t, "parse.jsonl")
	for i, c := range cases {
		opts := ParseOptions(c.Options)
		value, errs := Parse(c.Text, opts)
		name := fmt.Sprintf("case %d %q %+v", i, abbreviate(c.Text), opts)
		if c.Undefined {
			if value != nil {
				t.Errorf("%s: value = %#v, want nil (undefined)", name, value)
			}
		} else {
			got, err := Stringify(value)
			if err != nil {
				t.Errorf("%s: Stringify: %v", name, err)
			} else if got != *c.Value {
				t.Errorf("%s: value\n got %s\nwant %s", name, got, *c.Value)
			}
		}
		want := make([]ParseError, len(c.Errors))
		for j, e := range c.Errors {
			want[j] = ParseError{ParseErrorCode(e.Code), e.Offset, e.Length}
		}
		if len(errs) != len(want) || (len(want) > 0 && !reflect.DeepEqual(errs, want)) {
			t.Errorf("%s: errors\n got %v\nwant %v", name, errs, want)
		}
		// ParseTree reports the same errors as Parse.
		if _, treeErrs := ParseTree(c.Text, opts); !reflect.DeepEqual(treeErrs, errs) {
			t.Errorf("%s: ParseTree errors %v differ from Parse errors %v", name, treeErrs, errs)
		}
	}
}

func TestFormatFixtures(t *testing.T) {
	type formatCase struct {
		Text    string            `json:"text"`
		Options fixtureFormatting `json:"options"`
		Range   *Range            `json:"range"`
		Edits   []fixtureEdit     `json:"edits"`
		Error   string            `json:"error"`
	}
	_, cases := loadFixture[struct{}, formatCase](t, "format.jsonl")
	for i, c := range cases {
		name := fmt.Sprintf("case %d %q %+v range %v", i, abbreviate(c.Text), c.Options, c.Range)
		got, err := Format(c.Text, c.Range, c.Options.options())
		if c.Error != "" {
			if err == nil {
				t.Errorf("%s: want error (%s), got %d edits", name, c.Error, len(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if want := toEdits(c.Edits); len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("%s: edits\n got %+v\nwant %+v", name, got, want)
		}
	}
}

func TestModifyFixtures(t *testing.T) {
	type modifyCase struct {
		Doc     int `json:"doc"`
		Options struct {
			FormattingOptions *fixtureFormatting `json:"formattingOptions"`
		} `json:"options"`
		Ops []struct {
			Path   []any   `json:"path"`
			Value  *string `json:"value"`
			Remove bool    `json:"remove"`
		} `json:"ops"`
		Steps  [][]fixtureEdit `json:"steps"`
		Result *string         `json:"result"`
		Error  *struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	header, cases := loadFixture[struct {
		Docs []string `json:"docs"`
	}, modifyCase](t, "modify.jsonl")
	for i, c := range cases {
		var opts ModificationOptions
		if f := c.Options.FormattingOptions; f != nil {
			o := f.options()
			opts.FormattingOptions = &o
		}
		text := header.Docs[c.Doc]
		name := fmt.Sprintf("case %d doc %d %q", i, c.Doc, abbreviate(text))
		want := text // expected text, from the upstream edits when no result is stored
		var err error
		for j, op := range c.Ops {
			path := make([]any, len(op.Path))
			for k, seg := range op.Path {
				if f, ok := seg.(float64); ok {
					path[k] = int(f)
				} else {
					path[k] = seg
				}
			}
			name += fmt.Sprintf(" | %v", path)
			var edits []Edit
			if op.Remove {
				edits, err = Remove(text, path, opts)
			} else {
				value, perrs := Parse(*op.Value, ParseOptions{})
				if len(perrs) > 0 {
					t.Fatalf("%s: bad fixture value %s: %v", name, *op.Value, perrs)
				}
				edits, err = Modify(text, path, value, opts)
			}
			if err != nil {
				break
			}
			if j < len(c.Steps) {
				wantEdits := toEdits(c.Steps[j])
				if len(edits) != len(wantEdits) || (len(wantEdits) > 0 && !reflect.DeepEqual(edits, wantEdits)) {
					t.Errorf("%s: step %d edits\n got %+v\nwant %+v", name, j, edits, wantEdits)
				}
				want = splice(want, wantEdits)
			}
			if text, err = ApplyEdits(text, edits); err != nil {
				break
			}
		}
		switch {
		case c.Error != nil:
			if err == nil {
				t.Errorf("%s: want %s %q, got result %q", name, c.Error.Kind, c.Error.Message, text)
			} else if c.Error.Kind == "Error" && err.Error() != c.Error.Message {
				t.Errorf("%s: error %q, want %q", name, err, c.Error.Message)
			}
		case err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case c.Result != nil && text != *c.Result:
			t.Errorf("%s: result\n got %q\nwant %q", name, text, *c.Result)
		case text != want:
			t.Errorf("%s: result\n got %q\nwant %q", name, text, want)
		}
	}
}

// splice applies non-overlapping edits independently of ApplyEdits.
func splice(text string, edits []Edit) string {
	sorted := append([]Edit(nil), edits...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset > sorted[j].Offset })
	for _, e := range sorted {
		text = text[:e.Offset] + e.Content + text[e.Offset+e.Length:]
	}
	return text
}

func abbreviate(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// The behaviors below are what the CLI relies on; the fixtures cover them
// too, but these spell out the contract for readers of the Go API.
func TestCLIContract(t *testing.T) {
	fo := &FormattingOptions{InsertSpaces: true, TabSize: 2}
	server := NewObject().Set("type", "http").Set("url", "https://mcp.example/x")
	update := func(text string, path []any, value any) string {
		t.Helper()
		edits, err := Modify(text, path, value, ModificationOptions{FormattingOptions: fo})
		if err != nil {
			t.Fatal(err)
		}
		out, err := ApplyEdits(text, edits)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := update("{}", []any{"mcpServers", "durin"}, server)
	want := "{\n  \"mcpServers\": {\n    \"durin\": {\n      \"type\": \"http\",\n      \"url\": \"https://mcp.example/x\"\n    }\n  }\n}"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	got = update(got, []any{"list", 0}, []string{"a"})
	if !strings.Contains(got, "\"list\": [\n    [\n      \"a\"\n    ]\n  ]") {
		t.Fatalf("append into new array: %q", got)
	}
	v, errs := Parse("{\"b\":1,\"2\":0,\"a\":[1,true,null],\"b\":2,} // c", ParseOptions{AllowTrailingComma: true})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	obj := v.(*Object)
	if keys := obj.Keys(); !reflect.DeepEqual(keys, []string{"2", "b", "a"}) {
		t.Fatalf("keys %v", keys)
	}
	if b, _ := obj.Get("b"); b != 2.0 {
		t.Fatalf("duplicate key: b = %v", b)
	}
	if _, errs := Parse("{\"a\":1,}", ParseOptions{}); len(errs) == 0 {
		t.Fatal("trailing comma accepted without AllowTrailingComma")
	}
	if _, err := Stringify(map[string]any{}); err == nil {
		t.Fatal("unordered map accepted")
	}
}

// Stringify cases that only Go callers can produce (Parse never yields
// them): WTF-8 surrogates, invalid UTF-8, Go integer types and nil
// containers.
func TestStringifyGoValues(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{"\xed\xa0\xbd\xed\xb8\x80", `"😀"`},            // "\ud83d" + "\ude00" joins in JS
		{"\xed\xb8\x80\xed\xa0\xbd", `"\ude00\ud83d"`}, // reversed order stays unpaired
		{"a\xffb", "\"a\uFFFDb\""},
		{int64(1) << 53, "9007199254740992"},
		{uint8(7), "7"},
		{float32(0.5), "0.5"},
		{[]any(nil), "[]"},
		{(*Object)(nil), "null"},
		{Object{}, "{}"},
		{NewObject().Set("b", 1).Set("1", []string{"x"}).Set("a", nil).Set("0", true), `{"0":true,"1":["x"],"b":1,"a":null}`},
	} {
		got, err := Stringify(c.in)
		if err != nil || got != c.want {
			t.Errorf("Stringify(%#v) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

// Go-only API surface the fixtures cannot reach.
func TestGoAPI(t *testing.T) {
	o := NewObject().Set("x", 1).Set("y", 2).Set("x", 3)
	o.Delete("y")
	o.Delete("missing")
	if v, ok := o.Get("x"); !ok || v != 3 || o.Len() != 1 {
		t.Fatalf("object: %v %v %d", v, ok, o.Len())
	}
	var nilObj *Object
	if _, ok := nilObj.Get("x"); ok || nilObj.Len() != 0 || nilObj.Keys() != nil {
		t.Fatal("nil *Object should read as empty")
	}
	if _, err := ApplyEdits("abcd", []Edit{{Offset: 0, Length: 3}, {Offset: 2, Length: 1}}); err == nil {
		t.Fatal("overlapping edits accepted")
	}
	if got, err := ApplyEdits("abcd", []Edit{{Offset: 4, Content: "!"}, {Offset: 0, Length: 1, Content: "A"}}); err != nil || got != "Abcd!" {
		t.Fatalf("ApplyEdits = %q, %v", got, err)
	}
	if CommaExpected.String() != "CommaExpected" || ParseErrorCode(99).String() != "<unknown ParseErrorCode>" {
		t.Fatal("ParseErrorCode names")
	}
	if _, err := Modify("{}", []any{1.5}, 1, ModificationOptions{}); err == nil {
		t.Fatal("float path segment accepted")
	}
	if _, err := Modify("{}", []any{"a"}, struct{}{}, ModificationOptions{}); err == nil {
		t.Fatal("unsupported value type accepted")
	}
}
