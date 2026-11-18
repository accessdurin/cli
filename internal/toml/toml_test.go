package toml

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/jsjson"
)

// fixtures.json is produced by testdata/gen.mjs from smol-toml 1.9.1.
type fixtures struct {
	Parse []struct {
		Note   string   `json:"note"`
		B64    string   `json:"b64"`
		U16    []uint16 `json:"u16"`
		OK     bool     `json:"ok"`
		Dump   string   `json:"dump"`
		JSON   string   `json:"json"`
		Line   int      `json:"line"`
		Column int      `json:"column"`
	} `json:"parse"`
	Stringify []struct {
		Note  string          `json:"note"`
		Input json.RawMessage `json:"input"`
		OK    bool            `json:"ok"`
		Out   string          `json:"out"`
		Error string          `json:"error"`
	} `json:"stringify"`
	Dates []struct {
		U16 []uint16 `json:"u16"`
		T   *float64 `json:"t"`
	} `json:"dates"`
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

// dump mirrors dump() in gen.mjs.
func dump(v jsjson.Value) string {
	switch x := v.(type) {
	case *Date:
		iso := "ERR"
		if s, ok := x.ISOString(); ok {
			iso = jsjson.Quote(s)
		}
		flags := ""
		for _, f := range []bool{x.IsDate(), x.IsTime(), x.IsLocal(), x.IsDateTime()} {
			if f {
				flags += "1"
			} else {
				flags += "0"
			}
		}
		return "d" + iso + flags
	case jsjson.Array:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = dump(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *jsjson.Object:
		parts := []string{}
		for _, m := range x.Members() {
			parts = append(parts, jsjson.Quote(m.Key)+":"+dump(m.Value))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case jsjson.String:
		return jsjson.Quote(string(x))
	case jsjson.Number:
		if x == 0 && math.Signbit(float64(x)) {
			return "-0"
		}
		return jsjson.FormatNumber(float64(x))
	case jsjson.Bool:
		return strconv.FormatBool(bool(x))
	}
	return fmt.Sprintf("unexpected %T", v)
}

func TestParseMatchesSmolToml(t *testing.T) {
	f := loadFixtures(t)
	if len(f.Parse) < 5000 {
		t.Fatalf("corpus too small: %d", len(f.Parse))
	}
	for i, c := range f.Parse {
		var got *jsjson.Object
		var err error
		var src string
		if c.U16 != nil {
			src = jsjson.FromUTF16(c.U16)
			got, err = Parse(src)
		} else {
			b, _ := base64.StdEncoding.DecodeString(c.B64)
			src = string(b)
			got, err = ParseBytes(b)
		}
		if (err == nil) != c.OK {
			t.Errorf("case %d (%s) %q: accepted=%v, smol-toml accepted=%v (%v)", i, c.Note, src, err == nil, c.OK, err)
			continue
		}
		if err != nil {
			if e := err.(*Error); e.Line != c.Line || e.Column != c.Column {
				t.Errorf("case %d (%s) %q: error at %d:%d, smol-toml at %d:%d (%v)", i, c.Note, src, e.Line, e.Column, c.Line, c.Column, err)
			}
			continue
		}
		if d := dump(got); d != c.Dump {
			t.Errorf("case %d (%s) %q:\n got %s\nwant %s", i, c.Note, src, d, c.Dump)
		}
		// JSON.stringify(parse(src)), as bridge-config.ts compares entries.
		if j := jsjson.Stringify(got); j != c.JSON {
			t.Errorf("case %d (%s) %q JSON:\n got %s\nwant %s", i, c.Note, src, j, c.JSON)
		}
	}
}

// decodeInput mirrors build() in gen.mjs.
func decodeInput(t *testing.T, raw json.RawMessage) jsjson.Value {
	var e map[string]json.RawMessage
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	str := func(r json.RawMessage) string {
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			return s
		}
		var u []uint16
		if err := json.Unmarshal(r, &u); err != nil {
			t.Fatal(err)
		}
		return jsjson.FromUTF16(u)
	}
	switch {
	case e["o"] != nil:
		var pairs [][2]json.RawMessage
		if err := json.Unmarshal(e["o"], &pairs); err != nil {
			t.Fatal(err)
		}
		o := jsjson.NewObject()
		for _, p := range pairs {
			o.Set(str(p[0]), decodeInput(t, p[1]))
		}
		return o
	case e["a"] != nil:
		var items []json.RawMessage
		if err := json.Unmarshal(e["a"], &items); err != nil {
			t.Fatal(err)
		}
		a := jsjson.Array{}
		for _, it := range items {
			a = append(a, decodeInput(t, it))
		}
		return a
	case e["s"] != nil, e["su"] != nil:
		if e["s"] != nil {
			return jsjson.String(str(e["s"]))
		}
		return jsjson.String(str(e["su"]))
	case e["n"] != nil:
		var lit string
		if err := json.Unmarshal(e["n"], &lit); err != nil {
			t.Fatal(err)
		}
		switch lit {
		case "NaN":
			return jsjson.Number(math.NaN())
		case "Infinity":
			return jsjson.Number(math.Inf(1))
		case "-Infinity":
			return jsjson.Number(math.Inf(-1))
		}
		v, err := jsjson.ParseString(lit)
		if err != nil {
			t.Fatal(err)
		}
		return v
	case e["b"] != nil:
		return jsjson.Bool(string(e["b"]) == "true")
	case e["nul"] != nil:
		return jsjson.Null{}
	case e["undef"] != nil:
		return nil
	case e["d"] != nil:
		var lit string
		if err := json.Unmarshal(e["d"], &lit); err != nil {
			t.Fatal(err)
		}
		o, err := Parse("x = " + lit)
		if err != nil {
			t.Fatal(err)
		}
		v, _ := o.Get("x")
		return v
	}
	t.Fatalf("bad input %s", raw)
	return nil
}

func TestStringifyMatchesSmolToml(t *testing.T) {
	f := loadFixtures(t)
	for i, c := range f.Stringify {
		in := decodeInput(t, c.Input)
		out, err := Stringify(in)
		if (err == nil) != c.OK {
			t.Errorf("case %d (%s): ok=%v (%v), smol-toml ok=%v (%s)", i, c.Note, err == nil, err, c.OK, c.Error)
			continue
		}
		if err == nil && out != c.Out {
			t.Errorf("case %d (%s):\n got %q\nwant %q", i, c.Note, out, c.Out)
		}
		if err == nil && c.Note == "random" {
			// smol-toml output must parse back to the same document.
			back, perr := Parse(out)
			if perr != nil {
				t.Errorf("case %d: stringify output does not parse: %v", i, perr)
			} else if again, _ := Stringify(back); again != out {
				t.Errorf("case %d: not stable:\n%s\n%s", i, out, again)
			}
		}
	}
}

func TestBridgeConfigShape(t *testing.T) {
	// src/bridge-config.ts: `${raw}\n${stringify({ [serverKey]: { [name]: entry } })}`
	entry := jsjson.Obj(
		jsjson.KV("command", jsjson.String("/usr/bin/node")),
		jsjson.KV("args", jsjson.Strings("/opt/durin/cli.js", "mcp", "serve", "--profile", "0123abcd4567ef89", "--config-dir", "/home/u/.config/durin")),
	)
	out, err := Stringify(jsjson.Obj(jsjson.KV("mcp_servers", jsjson.Obj(jsjson.KV("durin-0123abcd4567", entry)))))
	if err != nil {
		t.Fatal(err)
	}
	want := "[mcp_servers.durin-0123abcd4567]\ncommand = \"/usr/bin/node\"\nargs = [ \"/opt/durin/cli.js\", \"mcp\", \"serve\", \"--profile\", \"0123abcd4567ef89\", \"--config-dir\", \"/home/u/.config/durin\" ]\n"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	doc, err := Parse("model = \"o3\"\n\n" + out)
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := doc.GetObject("mcp_servers")
	got, _ := servers.GetObject("durin-0123abcd4567")
	if jsjson.Stringify(got) != jsjson.Stringify(entry) {
		t.Errorf("round trip: %s", jsjson.Stringify(got))
	}
	if _, err := Parse(out + out); err == nil {
		t.Error("a duplicate table must be rejected")
	}
}

func TestDateParseMatchesV8(t *testing.T) {
	f := loadFixtures(t)
	if len(f.Dates) < 1000 {
		t.Fatalf("corpus too small: %d", len(f.Dates))
	}
	for _, c := range f.Dates {
		got := parseJSDate(c.U16)
		if c.T == nil {
			if !math.IsNaN(got) {
				t.Errorf("Date.parse(%q) = %v, want NaN", jsjson.FromUTF16(c.U16), got)
			}
		} else if got != *c.T {
			t.Errorf("Date.parse(%q) = %v, want %v", jsjson.FromUTF16(c.U16), got, *c.T)
		}
	}
}
