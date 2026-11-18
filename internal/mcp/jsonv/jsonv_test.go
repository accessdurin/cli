package jsonv

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestMatchesNode compares JSON.parse/JSON.stringify round trips, rejected inputs and
// Number#toString with Node (testdata/record.mjs).
func TestMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/roundtrip.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Roundtrip []struct{ Input, Output string } `json:"roundtrip"`
		Invalid   []struct {
			Input string `json:"input"`
			OK    bool   `json:"ok"`
		} `json:"invalid"`
		Numbers []struct {
			Bits []byte `json:"bits"`
			Text string `json:"text"`
		} `json:"numbers"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Roundtrip {
		v, err := ParseString(c.Input)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.Input, err)
			continue
		}
		if got := MustStringify(v); got != c.Output {
			t.Errorf("round trip %q:\n got  %q\n node %q", c.Input, got, c.Output)
		}
	}
	for _, c := range fx.Invalid {
		if _, err := ParseString(c.Input); (err == nil) != c.OK {
			t.Errorf("Parse(%q) ok=%v, node ok=%v", c.Input, err == nil, c.OK)
		}
	}
	for _, c := range fx.Numbers {
		f := math.Float64frombits(binary.LittleEndian.Uint64(c.Bits))
		if got := FormatNumber(f); got != c.Text {
			t.Errorf("FormatNumber(%v) = %q, node %q", f, got, c.Text)
		}
	}
}

func TestObjectOrderAndUndefined(t *testing.T) {
	o := NewObject("b", 1.0, "a", 2.0)
	o.Set("1", 3.0)
	o.Set("b", 4.0) // keeps its position
	o.Set("gone", Undefined)
	if got := MustStringify(o); got != `{"1":3,"b":4,"a":2}` {
		t.Fatalf("got %s", got)
	}
	o.Delete("b")
	if got := MustStringify(o); got != `{"1":3,"a":2}` {
		t.Fatalf("got %s", got)
	}
}
