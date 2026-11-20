package jsjson

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strconv"
	"testing"
)

// fixtures.json is produced by testdata/gen.mjs from Node 22 (V8's JSON and
// the CLI's own terminalJson/canonicalJson).
type fixtures struct {
	Parse []struct {
		Note      string   `json:"note"`
		B64       string   `json:"b64"`
		U16       []uint16 `json:"u16"`
		OK        bool     `json:"ok"`
		Compact   string   `json:"compact"`
		Indent    *string  `json:"indent"`
		Terminal  *string  `json:"terminal"`
		Canonical string   `json:"canonical"`
	} `json:"parse"`
	Numbers []struct {
		Bits string `json:"bits"`
		Str  string `json:"str"`
		JSON string `json:"json"`
	} `json:"numbers"`
	Decode []struct {
		B64 string   `json:"b64"`
		U16 []uint16 `json:"u16"`
	} `json:"decode"`
	Objects []struct {
		Ops       [][]any `json:"ops"`
		Compact   string  `json:"compact"`
		Canonical string  `json:"canonical"`
	} `json:"objects"`
}

func loadFixtures(t *testing.T) fixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseAndStringifyMatchNode(t *testing.T) {
	f := loadFixtures(t)
	if len(f.Parse) < 1000 {
		t.Fatalf("corpus too small: %d", len(f.Parse))
	}
	for i, c := range f.Parse {
		var v Value
		var err error
		var src string
		if c.U16 != nil {
			src = FromUTF16(c.U16)
			v, err = ParseString(src)
		} else {
			b, decErr := base64.StdEncoding.DecodeString(c.B64)
			if decErr != nil {
				t.Fatal(decErr)
			}
			src = string(b)
			v, err = Parse(b)
		}
		if (err == nil) != c.OK {
			t.Errorf("case %d (%s) %q: accepted=%v, Node accepted=%v (%v)", i, c.Note, src, err == nil, c.OK, err)
			continue
		}
		if !c.OK {
			continue
		}
		check := func(what, got, want string) {
			if got != want {
				t.Errorf("case %d (%s) %q %s:\n got %q\nwant %q", i, c.Note, src, what, got, want)
			}
		}
		check("Stringify", Stringify(v), c.Compact)
		check("CanonicalJSON", CanonicalJSON(v), c.Canonical)
		if c.Indent != nil {
			check("StringifyIndent", StringifyIndent(v, 2), *c.Indent)
			check("TerminalJSON", TerminalJSON(v), *c.Terminal)
		}
		// Re-parsing the output must give the same value back.
		again, err := ParseString(Stringify(v))
		if err != nil || Stringify(again) != c.Compact {
			t.Errorf("case %d: round trip failed: %v", i, err)
		}
	}
}

func TestFormatNumberMatchesNode(t *testing.T) {
	f := loadFixtures(t)
	for _, c := range f.Numbers {
		bits, err := strconv.ParseUint(c.Bits, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		x := math.Float64frombits(bits)
		if got := FormatNumber(x); got != c.Str {
			t.Errorf("FormatNumber(%s) = %q, want %q", c.Bits, got, c.Str)
		}
		if got := Stringify(Number(x)); got != c.JSON {
			t.Errorf("Stringify(%s) = %q, want %q", c.Bits, got, c.JSON)
		}
	}
}

func TestDecodeUTF8MatchesNode(t *testing.T) {
	f := loadFixtures(t)
	for _, c := range f.Decode {
		b, _ := base64.StdEncoding.DecodeString(c.B64)
		got := ToUTF16(DecodeUTF8(b))
		want := c.U16
		if want == nil {
			want = []uint16{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("DecodeUTF8(% x) = %x, want %x", b, got, want)
		}
	}
	if got := DecodeUTF8StripBOM([]byte("\xef\xbb\xbf{}")); got != "{}" {
		t.Errorf("DecodeUTF8StripBOM kept the BOM: %q", got)
	}
}

func TestObjectOperationsMatchNode(t *testing.T) {
	f := loadFixtures(t)
	for i, c := range f.Objects {
		o := NewObject()
		for _, op := range c.Ops {
			key := op[1].(string)
			if op[0] == "delete" {
				o.Delete(key)
			} else {
				o.Set(key, Number(op[2].(float64)))
			}
		}
		if got := Stringify(o); got != c.Compact {
			t.Errorf("objects[%d] %v:\n got %s\nwant %s", i, c.Ops, got, c.Compact)
		}
		if got := CanonicalJSON(o); got != c.Canonical {
			t.Errorf("objects[%d] canonical:\n got %s\nwant %s", i, got, c.Canonical)
		}
	}
}

type date struct {
	Extension
	iso string
}

func (d date) ToJSON() Value { return String(d.iso) }

func TestBuildersAndUndefined(t *testing.T) {
	o := Obj(
		KV("b", Strings("x", "y")),
		KV("skip", nil),
		KV("2", Int(2)),
		KV("a", Obj()),
		KV("1", Arr(nil, Null{}, Bool(true), Number(math.Inf(1)), Number(math.Copysign(0, -1)))),
		KV("when", date{iso: "1979-05-27"}),
	)
	if got, want := Stringify(o), `{"1":[null,null,true,null,0],"2":2,"b":["x","y"],"a":{},"when":"1979-05-27"}`; got != want {
		t.Errorf("Stringify = %s, want %s", got, want)
	}
	if got, want := CanonicalJSON(o), `{"1":[null,null,true,null,0],"2":2,"a":{},"b":["x","y"],"when":{}}`; got != want {
		t.Errorf("CanonicalJSON = %s, want %s", got, want)
	}
	if got, want := StringifyIndent(Obj(KV("a", Obj()), KV("b", Arr())), 2), "{\n  \"a\": {},\n  \"b\": []\n}"; got != want {
		t.Errorf("StringifyIndent = %q, want %q", got, want)
	}
	if got := Stringify(nil); got != "" {
		t.Errorf("Stringify(undefined) = %q", got)
	}
	if got := StringifyIndent(Arr(Int(1)), 20); got != "[\n          1\n]" {
		t.Errorf("indent is not clamped to 10: %q", got)
	}
	if s, ok := o.GetString("b"); ok || s != "" {
		t.Error("GetString on an array")
	}
	if a, ok := o.GetArray("b"); !ok || len(a) != 2 {
		t.Error("GetArray")
	}
	if !o.Has("skip") || TypeOf(nil) != "undefined" || TypeOf(Null{}) != "object" {
		t.Error("undefined members are own keys")
	}
	c := o.Clone()
	c.Delete("b")
	if !o.Has("b") || c.Has("b") || c.Len() != o.Len()-1 {
		t.Error("Clone is not independent")
	}
	if !Equal(o, o.Clone()) || Equal(o, c) {
		t.Error("Equal")
	}
	// Object.assign keeps target positions and appends new keys.
	var z Object
	z.Set("x", Int(1)).Set("y", Bool(true)).Assign(Obj(KV("y", Bool(false)), KV("0", Obj()), KV("w", Int(2))))
	keys := []string{}
	z.Range(func(k string, _ Value) bool { keys = append(keys, k); return k != "y" })
	if !slices.Equal(keys, []string{"0", "x", "y"}) || Stringify(&z) != `{"0":{},"x":1,"y":false,"w":2}` {
		t.Errorf("Assign/Range: %v %s", keys, Stringify(&z))
	}
	if n, ok := z.GetNumber("x"); !ok || n != 1 {
		t.Error("GetNumber")
	}
	if b, ok := z.GetBool("y"); !ok || b {
		t.Error("GetBool")
	}
	if _, ok := z.GetObject("0"); !ok {
		t.Error("GetObject")
	}
	if _, ok := z.GetObject("x"); ok {
		t.Error("GetObject on a number")
	}
}

func TestStringRepresentation(t *testing.T) {
	lone := FromUTF16([]uint16{0xD800})
	if lone != "\xed\xa0\x80" {
		t.Fatalf("lone surrogate is %x", lone)
	}
	// A pair assembled from halves is canonical UTF-8.
	if got := FromUTF16([]uint16{0xD83D, 0xDE00}); got != "😀" {
		t.Errorf("pair = %q", got)
	}
	split := "\xed\xa0\xbd\xed\xb8\x80" // non-canonical halves of U+1F600
	if Quote(split) != `"😀"` || !IsWellFormed(split) || UTF16Len(split) != 2 {
		t.Errorf("split pair not treated as a pair: %s", Quote(split))
	}
	if Quote(lone+"x") != `"\ud800x"` || IsWellFormed(lone) || ToWellFormed(lone+"a") != "\uFFFDa" {
		t.Error("lone surrogate handling")
	}
	if CompareUTF16("\uFF01", "😀") <= 0 || CompareUTF16("a", "b") >= 0 || CompareUTF16("ab", "a") <= 0 {
		t.Error("CompareUTF16 is not UTF-16 order")
	}
	// Bytes that are not WTF-8 read as U+FFFD, like Node would decode them.
	if got := Quote("a\xffb\xe2\x82"); got != "\"a\uFFFDb\uFFFD\"" {
		t.Errorf("invalid bytes: %q", got)
	}
}

func TestGoConversion(t *testing.T) {
	type entry struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Enabled bool     `json:"enabled,omitempty"`
		N       *int     `json:"n"`
	}
	v, err := FromGo(map[string]any{"z": 1, "a": entry{Command: "<node>", Args: []string{"x"}}, "3": nil, "n": json.Number("1e400")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Stringify(v), `{"3":null,"a":{"command":"<node>","args":["x"],"n":null},"n":null,"z":1}`; got != want {
		t.Errorf("FromGo = %s, want %s", got, want)
	}
	var back struct {
		A entry `json:"a"`
	}
	if err := Decode(v, &back); err != nil || back.A.Command != "<node>" || back.A.Args[0] != "x" {
		t.Errorf("Decode = %+v, %v", back, err)
	}
	g := ToGo(v).(map[string]any)
	if g["z"] != 1.0 || g["3"] != nil {
		t.Errorf("ToGo = %v", g)
	}
}
