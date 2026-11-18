package oauth

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/accessdurin/cli/internal/mcp/jsonv"
)

type schemaCase struct {
	Input  string  `json:"input"`
	OK     bool    `json:"ok"`
	Output *string `json:"output"`
}

// TestStoredCredentialSchemasMatchSDK checks OAuthTokensSchema, OAuthClientInformationSchema and
// OAuthClientInformationFullSchema against the SDK (testdata/record.mjs): which stored values
// parse, and the exact JSON of the parsed value (stripped keys, coerced expires_in, dropped
// non-string issuer, trimmed URLs, "" logo_uri read as absent).
func TestStoredCredentialSchemasMatchSDK(t *testing.T) {
	raw, err := os.ReadFile("testdata/schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct{ Tokens, Clients, Full []schemaCase }
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	check := func(kind string, cases []schemaCase, parse func([]byte) (any, error)) {
		for _, c := range cases {
			out, err := parse([]byte(c.Input))
			if (err == nil) != c.OK {
				t.Errorf("%s %s: ok=%v (%v), SDK ok=%v", kind, c.Input, err == nil, err, c.OK)
				continue
			}
			if !c.OK {
				continue
			}
			got, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != *c.Output {
				t.Errorf("%s %s:\n got %s\n SDK %s", kind, c.Input, got, *c.Output)
			}
		}
	}
	check("tokens", fx.Tokens, func(b []byte) (any, error) { return ParseTokensJSON(b) })
	check("client", fx.Clients, func(b []byte) (any, error) {
		v, err := jsonv.Parse(b)
		if err != nil {
			return nil, err
		}
		return ParseClientInformation(v)
	})
	check("full", fx.Full, func(b []byte) (any, error) {
		v, err := jsonv.Parse(b)
		if err != nil {
			return nil, err
		}
		return ParseClientInformationFull(v)
	})
	var tok Tokens
	if err := json.Unmarshal([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":"60","x":1}`), &tok); err != nil || *tok.ExpiresIn != 60 {
		t.Fatalf("json.Unmarshal applies the schema: %+v %v", tok, err)
	}
}

func TestWWWAuthenticateAndScopes(t *testing.T) {
	h := http.Header{}
	h.Add("WWW-Authenticate", `Bearer error="insufficient_scope", scope="a b", resource_metadata="https://x.example/prm", error_description=need`)
	p := ExtractWWWAuthenticateParams(h)
	if p.Error != "insufficient_scope" || p.Scope != "a b" || p.ResourceMetadataURL.Href() != "https://x.example/prm" || p.ErrorDescription != "need" {
		t.Fatalf("%+v", p)
	}
	h.Set("WWW-Authenticate", `Basic realm="x", scope="a"`)
	if p := ExtractWWWAuthenticateParams(h); p.Scope != "" {
		t.Fatalf("non-bearer challenge parsed: %+v", p)
	}
	if u := ComputeScopeUnion("a b", "", "b c"); u != "a b c" {
		t.Fatalf("union %q", u)
	}
	if !IsStrictScopeSuperset("a b", "a") || IsStrictScopeSuperset("a", "a b") || IsStrictScopeSuperset("", "") {
		t.Fatal("IsStrictScopeSuperset")
	}
	urls, err := BuildDiscoveryURLs("https://a.example/t/")
	if err != nil || len(urls) != 3 || urls[0] != "https://a.example/.well-known/oauth-authorization-server/t" || urls[2] != "https://a.example/t/.well-known/openid-configuration" {
		t.Fatalf("%v %v", urls, err)
	}
}
