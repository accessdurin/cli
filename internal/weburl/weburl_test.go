package weburl

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The fixtures are written by testdata/gen.mjs from Node 22's own URL, URLSearchParams and
// zod's z.url(); run it again after changing the corpus. go test never needs Node.

type snapshot struct {
	Href     string `json:"href"`
	Origin   string `json:"origin"`
	Protocol string `json:"protocol"`
	Username string `json:"username"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Hostname string `json:"hostname"`
	Port     string `json:"port"`
	Pathname string `json:"pathname"`
	Search   string `json:"search"`
	Hash     string `json:"hash"`
}

func snap(u *URL) snapshot {
	return snapshot{
		Href: u.Href(), Origin: u.Origin(), Protocol: u.Protocol(), Username: u.Username(),
		Password: u.Password(), Host: u.Host(), Hostname: u.Hostname(), Port: u.Port(),
		Pathname: u.Pathname(), Search: u.Search(), Hash: u.Hash(),
	}
}

func diff(got, want snapshot) string {
	var out []string
	g, w := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := 0; i < g.NumField(); i++ {
		if g.Field(i).String() != w.Field(i).String() {
			out = append(out, fmt.Sprintf("%s: got %q, want %q",
				g.Type().Field(i).Name, g.Field(i).String(), w.Field(i).String()))
		}
	}
	return strings.Join(out, "; ")
}

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

type parseCase struct {
	snapshot
	Source       string      `json:"source"`
	Input        string      `json:"input"`
	Base         *string     `json:"base"`
	Failure      bool        `json:"failure"`
	SearchParams [][2]string `json:"searchParams"`
}

func (c parseCase) String() string {
	if c.Base != nil {
		return fmt.Sprintf("%s new URL(%q, %q)", c.Source, c.Input, *c.Base)
	}
	return fmt.Sprintf("%s new URL(%q)", c.Source, c.Input)
}

func TestParseMatchesNode(t *testing.T) {
	var f struct {
		Meta  struct{ Node, Ada string }
		Cases []parseCase
	}
	load(t, "parse.json", &f)
	// idna_tables.go comes from the ada release this Node embeds (gen_idna_tables.go).
	if !strings.HasPrefix(f.Meta.Node, "v22.") || f.Meta.Ada != "2.9.2" || len(f.Cases) < 25000 {
		t.Fatalf("fixtures from Node %s (ada %s) with %d cases; regenerate with Node 22",
			f.Meta.Node, f.Meta.Ada, len(f.Cases))
	}
	for _, c := range f.Cases {
		var u *URL
		var err error
		if c.Base != nil {
			u, err = ParseRef(c.Input, *c.Base)
			if CanParseRef(c.Input, *c.Base) != (err == nil) {
				t.Errorf("%v: CanParseRef disagrees with ParseRef", c)
			}
		} else {
			u, err = Parse(c.Input)
			if CanParse(c.Input) != (err == nil) {
				t.Errorf("%v: CanParse disagrees with Parse", c)
			}
		}
		if c.Failure {
			if err == nil {
				t.Errorf("%v: parsed to %q, Node rejects it", c, u.Href())
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v, Node parses it to %q", c, err, c.Href)
			continue
		}
		if d := diff(snap(u), c.snapshot); d != "" {
			t.Errorf("%v: %s", c, d)
		}
		if got := u.SearchParams().Entries(); len(got)+len(c.SearchParams) > 0 &&
			!reflect.DeepEqual(got, c.SearchParams) {
			t.Errorf("%v: searchParams %q, want %q", c, got, c.SearchParams)
		}
		// Node re-parses its own href to the same URL; the CLI relies on that idempotence.
		if again, err := Parse(u.Href()); err != nil || again.Href() != u.Href() {
			t.Errorf("%v: href %q does not re-parse to itself", c, u.Href())
		}
	}
}

func TestSettersMatchNode(t *testing.T) {
	var f struct {
		Cases []struct {
			Source, Setter, Href, Value string
			Expected                    snapshot
		}
	}
	load(t, "setters.json", &f)
	for _, c := range f.Cases {
		u, err := Parse(c.Href)
		if err != nil {
			t.Fatalf("%q: %v", c.Href, err)
		}
		switch c.Setter {
		case "hostname":
			u.SetHostname(c.Value)
		case "host":
			u.SetHost(c.Value)
		case "search":
			u.SetSearch(c.Value)
		case "hash":
			u.SetHash(c.Value)
		default:
			t.Fatalf("unknown setter %q", c.Setter)
		}
		if d := diff(snap(u), c.Expected); d != "" {
			t.Errorf("%s: url = new URL(%q); url.%s = %q: %s", c.Source, c.Href, c.Setter, c.Value, d)
		}
	}
}

func TestSearchParamsMatchNode(t *testing.T) {
	var f struct {
		Parse []struct {
			Input   string
			Size    int
			Lookups []struct {
				Name   string
				Get    *string
				GetAll []string
				Has    bool
			}
			Entries [][2]string
			Object  [][2]string
			String  string
		}
		Ops []struct {
			Input   string
			Ops     [][]string
			Entries [][2]string
			String  string
		}
		Serialize []struct {
			Pairs  [][2]string
			String string
		}
	}
	load(t, "searchparams.json", &f)
	for _, c := range f.Parse {
		p := ParseQuery(c.Input)
		if p.Size() != c.Size {
			t.Errorf("new URLSearchParams(%q).size = %d, want %d", c.Input, p.Size(), c.Size)
		}
		for _, l := range c.Lookups {
			value, ok := p.Get(l.Name)
			if ok != (l.Get != nil) || (ok && value != *l.Get) || p.Has(l.Name) != l.Has {
				t.Errorf("new URLSearchParams(%q): get/has(%q) = %q, %v; want %v, %v", c.Input, l.Name, value, ok, l.Get, l.Has)
			}
			if got := p.GetAll(l.Name); len(got)+len(l.GetAll) > 0 && !reflect.DeepEqual(got, l.GetAll) {
				t.Errorf("new URLSearchParams(%q).getAll(%q) = %q, want %q", c.Input, l.Name, got, l.GetAll)
			}
		}
		if got := p.Entries(); len(got)+len(c.Entries) > 0 && !reflect.DeepEqual(got, c.Entries) {
			t.Errorf("new URLSearchParams(%q): entries %q, want %q", c.Input, got, c.Entries)
		}
		var object [][2]string
		for k, v := range p.Object() {
			object = append(object, [2]string{k, v})
		}
		sort.Slice(object, func(i, j int) bool { return object[i][0] < object[j][0] })
		if len(object)+len(c.Object) > 0 && !reflect.DeepEqual(object, c.Object) {
			t.Errorf("Object.fromEntries(new URLSearchParams(%q)) = %q, want %q", c.Input, object, c.Object)
		}
		if got := p.String(); got != c.String {
			t.Errorf("new URLSearchParams(%q).toString() = %q, want %q", c.Input, got, c.String)
		}
	}
	for _, c := range f.Ops {
		p := ParseQuery(c.Input)
		for _, op := range c.Ops {
			switch op[0] {
			case "set":
				p.Set(op[1], op[2])
			case "append":
				p.Append(op[1], op[2])
			case "delete":
				p.Delete(op[1])
			}
		}
		if got := p.Entries(); p.String() != c.String || (len(got)+len(c.Entries) > 0 && !reflect.DeepEqual(got, c.Entries)) {
			t.Errorf("%q after %q: %q (%q), want %q (%q)", c.Input, c.Ops, got, p.String(), c.Entries, c.String)
		}
	}
	for _, c := range f.Serialize {
		if got := NewSearchParams(c.Pairs...).String(); got != c.String {
			t.Errorf("new URLSearchParams(%q).toString() = %q, want %q", c.Pairs, got, c.String)
		}
	}
}

func TestZodURLMatchesZod(t *testing.T) {
	var f struct {
		Cases []struct {
			Input, Value string
			OK           bool
		}
	}
	load(t, "zod.json", &f)
	for _, c := range f.Cases {
		value, ok := ZodURL(c.Input)
		if ok != c.OK || value != c.Value || IsZodURL(c.Input) != c.OK {
			t.Errorf("z.url().safeParse(%q) = %q, %v; zod gives %q, %v", c.Input, value, ok, c.Value, c.OK)
		}
	}
}
