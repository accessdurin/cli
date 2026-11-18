package jsurl

import (
	"encoding/json"
	"os"
	"testing"
)

type urlCase struct {
	Input    string  `json:"input"`
	Base     *string `json:"base"`
	OK       bool    `json:"ok"`
	Href     string  `json:"href"`
	Origin   string  `json:"origin"`
	Protocol string  `json:"protocol"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	Host     string  `json:"host"`
	Hostname string  `json:"hostname"`
	Port     string  `json:"port"`
	Pathname string  `json:"pathname"`
	Search   string  `json:"search"`
	Hash     string  `json:"hash"`
}

type fixture struct {
	Cases  []urlCase `json:"cases"`
	Params []struct {
		Query    string      `json:"query"`
		Pairs    [][2]string `json:"pairs"`
		AfterSet string      `json:"afterSet"`
	} `json:"params"`
	AuthorizationURL string `json:"authorizationUrl"`
}

// TestMatchesNode compares parsing against Node's URL class, recorded by testdata/record.mjs.
func TestMatchesNode(t *testing.T) {
	raw, err := os.ReadFile("testdata/urls.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		var base *URL
		if c.Base != nil {
			base = MustParse(*c.Base)
		}
		u, err := Parse(c.Input, base)
		if (err == nil) != c.OK {
			t.Errorf("Parse(%q, %v): ok=%v, Node ok=%v", c.Input, c.Base, err == nil, c.OK)
			continue
		}
		if !c.OK {
			continue
		}
		got := urlCase{Input: c.Input, Base: c.Base, OK: true, Href: u.Href(), Origin: u.Origin(), Protocol: u.Protocol(),
			Username: u.Username(), Password: u.Password(), Host: u.Host(), Hostname: u.Hostname(), Port: u.Port(),
			Pathname: u.Pathname(), Search: u.Search(), Hash: u.Hash()}
		if got != c {
			t.Errorf("Parse(%q, %v):\n got  %+v\n node %+v", c.Input, c.Base, got, c)
		}
	}
	for _, p := range fx.Params {
		pairs := ParseQuery(p.Query)
		if len(pairs) != len(p.Pairs) {
			t.Errorf("ParseQuery(%q) = %v, want %v", p.Query, pairs, p.Pairs)
			continue
		}
		for i := range pairs {
			if pairs[i] != p.Pairs[i] {
				t.Errorf("ParseQuery(%q)[%d] = %v, want %v", p.Query, i, pairs[i], p.Pairs[i])
			}
		}
		sp := &SearchParams{list: pairs}
		sp.Set("a", "v w/é~!*()'")
		sp.Append("c", "")
		if sp.String() != p.AfterSet {
			t.Errorf("serialize(%q) = %q, want %q", p.Query, sp.String(), p.AfterSet)
		}
	}
	auth := MustParse("https://auth.example.test/authorize?existing=a b")
	sp := auth.SearchParams()
	sp.Set("response_type", "code")
	sp.Set("redirect_uri", "http://127.0.0.1:5555/oauth/callback")
	sp.Set("scope", "a b offline_access")
	sp.Append("prompt", "consent")
	if auth.Href() != fx.AuthorizationURL {
		t.Errorf("authorization URL = %q, want %q", auth.Href(), fx.AuthorizationURL)
	}
}
