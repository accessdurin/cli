package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

// Ports the tests accessdurin/cli#9 added for saved issuers, refused profiles and refused
// logout (src/refused-profile.test.ts, src/refused-logout.test.ts, src/host-pin-acceptance.test.ts).

const (
	refusedAccess       = "bearer-OLD-ACCESS-9f3c"
	refusedRefresh      = "refresh-OLD-9f3c"
	refusedPersonalPath = "/mcp/u/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// routes answers by URL path (or host+path) and records each request as "METHOD url body".
type routes struct {
	mu     sync.Mutex
	sent   []string
	answer func(req *http.Request) (*http.Response, error)
	gate   chan struct{}
}

func (r *routes) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.gate != nil {
		<-r.gate
	}
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	r.mu.Lock()
	r.sent = append(r.sent, strings.TrimSpace(req.Method+" "+req.URL.String()+" "+body))
	r.mu.Unlock()
	if r.answer == nil {
		return jsonAnswer(200, `{}`, req), nil
	}
	return r.answer(req)
}

func (r *routes) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func jsonAnswer(status int, body string, req *http.Request) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

// --- MCP issuer discovery ----------------------------------------------------------------------

func protectedResource(resource, issuer string) string {
	return `{"resource":"` + resource + `","authorization_servers":["` + issuer + `"]}`
}

func discoveryRoutes(resource string, path func() (int, string), root func() (*http.Response, error)) *routes {
	r := &routes{}
	r.answer = func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/.well-known/oauth-protected-resource" + refusedPersonalPath:
			status, body := path()
			return jsonAnswer(status, body, req), nil
		case "/.well-known/oauth-protected-resource":
			resp, err := root()
			if resp != nil {
				resp.Request = req
			}
			return resp, err
		}
		return jsonAnswer(404, `{}`, req), nil
	}
	return r
}

var productionScope = contracts.CliHostScope{AppOrigin: "https://app.getdurin.com"}

const productionMcp = "https://mcp.getdurin.com" + refusedPersonalPath

func TestMcpIssuerPinsTheProtectedResourceDocumentsIssuer(t *testing.T) {
	ctx := context.Background()
	trusted := func() (*http.Response, error) {
		return jsonAnswer(200, protectedResource(productionMcp, "https://auth.getdurin.com"), nil), nil
	}
	// A trailing slash is the same production issuer, saved without it.
	r := discoveryRoutes(productionMcp, func() (int, string) { return 200, protectedResource(productionMcp, "https://auth.getdurin.com/") }, trusted)
	if issuer, err := mcpIssuer(ctx, productionMcp, &http.Client{Transport: r}, productionScope); err != nil || issuer != "https://auth.getdurin.com" {
		t.Fatalf("issuer %q, %v", issuer, err)
	}
	// A path-aware 404 is never pinned, whatever it names: only the root document counts.
	for _, named := range []string{"https://auth.getdurin.com/", "https://evil.example"} {
		r := discoveryRoutes(productionMcp, func() (int, string) { return 404, protectedResource(productionMcp, named) }, trusted)
		if issuer, err := mcpIssuer(ctx, productionMcp, &http.Client{Transport: r}, productionScope); err != nil || issuer != "https://auth.getdurin.com" {
			t.Fatalf("404 naming %s: issuer %q, %v", named, issuer, err)
		}
		if strings.Contains(strings.Join(r.requests(), "\n"), "evil.example") {
			t.Fatalf("contacted the attacker: %v", r.requests())
		}
	}
	// When the root document fails, nothing is pinned and the attacker is never named.
	for name, root := range map[string]func() (*http.Response, error){
		"a network error": func() (*http.Response, error) { return nil, errors.New("fetch failed") },
		"HTTP 500":        func() (*http.Response, error) { return jsonAnswer(500, ``, nil), nil },
	} {
		r := discoveryRoutes(productionMcp, func() (int, string) { return 404, protectedResource(productionMcp, "https://evil.example") }, root)
		issuer, err := mcpIssuer(ctx, productionMcp, &http.Client{Transport: r}, productionScope)
		if err == nil || strings.Contains(err.Error(), "evil.example") {
			t.Fatalf("%s: issuer %q, %v", name, issuer, err)
		}
	}
}

func TestMcpIssuerRefusesALooseSelfHostedSpelling(t *testing.T) {
	ctx := context.Background()
	const app = "https://durin.acme.test"
	resource := app + refusedPersonalPath
	scope := contracts.CliHostScope{AppOrigin: app}
	run := func(issuer string) (string, error) {
		root := func() (*http.Response, error) { return jsonAnswer(404, `{}`, nil), nil }
		r := discoveryRoutes(resource, func() (int, string) { return 200, protectedResource(resource, issuer) }, root)
		return mcpIssuer(ctx, resource, &http.Client{Transport: r}, scope)
	}
	for _, issuer := range []string{app, app + "/"} {
		if got, err := run(issuer); err != nil || got != issuer {
			t.Errorf("%s: saved %q, %v", issuer, got, err)
		}
	}
	for _, issuer := range []string{"HTTPS://durin.acme.test/", "https://durin.acme.test:443/", "https://durin.acme.test//", `https://durin.acme.test/\u0000`} {
		_, err := run(issuer)
		wantCliError(t, err, "must be written exactly as https://durin.acme.test: lowercase, with no :443, //, spaces, NUL or backslash.")
	}
}

func TestAgreedIssuerFailsClosed(t *testing.T) {
	const disagree = "protected-resource documents that name different sign-in servers"
	_, err := agreedIssuer(&notedIssuers{}, "https://auth.getdurin.com", productionScope)
	wantCliError(t, err, disagree)
	_, err = agreedIssuer(&notedIssuers{pinned: "https://auth.getdurin.com", others: []string{"https://auth.getdurin.com/"}}, "https://auth.getdurin.com/", productionScope)
	if err != nil {
		t.Errorf("one trailing slash refused: %v", err)
	}
	_, err = agreedIssuer(&notedIssuers{pinned: "https://auth.getdurin.com", others: []string{"https://evil.example"}}, "https://auth.getdurin.com", productionScope)
	wantCliError(t, err, disagree)
	// A pinned issuer on another host is reported as that host, not as a disagreement.
	_, err = agreedIssuer(&notedIssuers{pinned: "https://evil.example"}, "https://auth.getdurin.com", productionScope)
	wantCliError(t, err, "evil.example")
}

// --- refused profiles ------------------------------------------------------------------------

// refusedProfile writes a production profile whose issuers are issuer (refused unless exactly
// https://auth.getdurin.com), makes it active, and stores a grant for it.
func refusedProfile(t *testing.T, issuer string, extra map[string]string) (dir, id string, store *memoryStore) {
	t.Helper()
	dir = tempDir(t)
	scope := jsjson.Obj(
		jsjson.KV("appOrigin", jsjson.String("https://app.getdurin.com")),
		jsjson.KV("accountIssuer", jsjson.String(issuer)),
		jsjson.KV("accountClientId", jsjson.String(refusedAccess+" "+refusedRefresh)),
		jsjson.KV("principalId", jsjson.String("user_one")),
		jsjson.KV("workosOrganizationId", jsjson.String("org_one")),
		jsjson.KV("organizationId", jsjson.String("org_internal")),
		jsjson.KV("environment", jsjson.String("production")),
		jsjson.KV("resource", jsjson.String(productionMcp)),
		jsjson.KV("mcpIssuer", jsjson.String(issuer)),
		jsjson.KV("development", jsjson.Bool(false)),
	)
	for key, value := range extra {
		scope.Set(key, jsjson.String(value))
	}
	id = textDigest(jsjson.Stringify(scope))
	writeFile(t, filepath.Join(dir, "profiles", id+".json"), jsjson.Stringify(scope.Clone().Set("version", jsjson.Int(1)).Set("id", jsjson.String(id))))
	writeFile(t, filepath.Join(dir, "active"), id+"\n")
	store = newMemoryStore()
	store.values["mcp:"+id+":tokens"] = `{"access_token":"` + refusedAccess + `","refresh_token":"` + refusedRefresh + `","token_type":"Bearer"}`
	store.values["mcp:"+id+":client"] = `{"client_id":"client_one"}`
	return dir, id, store
}

func seedUnrevoked(t *testing.T, dir, id string, store *memoryStore) {
	t.Helper()
	const grant = "11111111-1111-4111-8111-111111111111"
	store.values["mcp:"+id+":unrevoked:"+grant] = `{"accessToken":"` + refusedAccess + `","refreshToken":"` + refusedRefresh + `"}`
	writeFile(t, filepath.Join(dir, "revocations", id+".unrevoked.json"), `{"grants":[{"id":"`+grant+`","clientId":"client_one","addedAt":"2026-01-01T00:00:00.000Z"}]}`+"\n")
	writeFile(t, filepath.Join(dir, "revocations", id+".pending"), "{}\n")
}

func cliArgs(t *testing.T, args ...string) *CliOptions {
	t.Helper()
	parsed, err := ParseCliOptions(args)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestProfilesListKeepsGoodProfilesAndNamesRefusedOnes(t *testing.T) {
	dir, id, _ := refusedProfile(t, "https://auth.getdurin.com/", nil)
	good := mustProfile(t, testScope)
	if err := NewFileProfiles(dir).Save(good); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := ProfileCommand(cliArgs(t, "profiles", "list", "--config-dir", dir), &IO{Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("not an array: %s", out.String())
	}
	var accepted, refused map[string]any
	for _, row := range rows {
		switch row["id"] {
		case good.ID:
			accepted = row
		case id:
			refused = row
		}
	}
	if accepted == nil || accepted["refused"] != nil || accepted["mcpIssuer"] != good.McpIssuer {
		t.Errorf("accepted row: %v", accepted)
	}
	reason, _ := refused["reason"].(string)
	if refused["refused"] != true || !strings.Contains(reason, "must be exactly auth.getdurin.com over HTTPS") || !strings.Contains(reason, "Run durin to sign in again.") {
		t.Errorf("refused row: %v", refused)
	}
	if strings.Contains(out.String(), refusedAccess) || strings.Contains(out.String(), refusedRefresh) {
		t.Error("the list printed a stored token")
	}
	// An all-accepted list stays a flat array of profiles.
	if err := os.Remove(filepath.Join(dir, "profiles", id+".json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := ProfileCommand(cliArgs(t, "profiles", "list", "--config-dir", dir), &IO{Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if want := jsjson.StringifyIndent(jsjson.Array{good.JSON()}, 2); strings.TrimSpace(out.String()) != want {
		t.Errorf("all accepted:\n%s\nwant\n%s", out.String(), want)
	}
}

func TestCommandsNameARefusedSavedIssuer(t *testing.T) {
	dir, _, _ := refusedProfile(t, "https://auth.getdurin.com/", nil)
	_, err := ConnectionProfile(cliArgs(t, "tools", "list", "--config-dir", dir))
	wantCliError(t, err, "must be exactly auth.getdurin.com over HTTPS")
	wantCliError(t, err, "Run durin to sign in again.")
	if strings.Contains(err.Error(), refusedAccess) || strings.Contains(err.Error(), "user_one") {
		t.Errorf("the error leaked profile contents: %v", err)
	}
}

// --- refused logout ----------------------------------------------------------------------------

func TestPinnedHTTPSHost(t *testing.T) {
	for endpoint, want := range map[string]bool{
		"https://auth.getdurin.com":                           true,
		"https://auth.getdurin.com/oauth2/revoke":             true,
		"http://auth.getdurin.com":                            false,
		"https://user@auth.getdurin.com":                      false,
		"https://user:secret@auth.getdurin.com/oauth2/revoke": false,
		"https://auth.getdurin.com:443":                       false,
		"https://auth.getdurin.com:443/oauth2/revoke":         false,
		"https://auth.getdurin.com:8443":                      false,
		"https://AUTH.getdurin.com":                           false,
		"https://auth.getdurin.com.":                          false,
		"https://auth.getdurin.com.evil.com":                  false,
		"https://evilauth.getdurin.com":                       false,
		"https://evil.example":                                false,
	} {
		if got := pinnedHTTPSHost(endpoint); got != want {
			t.Errorf("pinnedHTTPSHost(%q) = %v, want %v", endpoint, got, want)
		}
	}
}

const productionMetadata = `"issuer":"https://auth.getdurin.com","authorization_endpoint":"https://auth.getdurin.com/oauth2/authorize","token_endpoint":"https://auth.getdurin.com/oauth2/token","response_types_supported":["code"]`

// metadataRoutes answers discovery with production metadata advertising revocation (when set)
// and every other request with revokeStatus.
func metadataRoutes(revocation string, revokeStatus int) *routes {
	r := &routes{}
	r.answer = func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, ".well-known") {
			extra := ""
			if revocation != "" {
				extra = `,"revocation_endpoint":"` + revocation + `"`
			}
			return jsonAnswer(200, `{`+productionMetadata+extra+`}`, req), nil
		}
		return jsonAnswer(revokeStatus, ``, req), nil
	}
	return r
}

func logoutRefused(t *testing.T, dir, id string, store *memoryStore, r *routes, args ...string) (string, *IO, error) {
	t.Helper()
	if len(args) == 0 {
		args = []string{"logout", "--profile", id, "--config-dir", dir}
	}
	opts := cliArgs(t, args...)
	_, refused := LoadSelectedProfile(opts)
	if refused == nil {
		t.Fatal("the profile was not refused")
	}
	var out bytes.Buffer
	io := &IO{Stdout: &out}
	err := logoutRefusedProfile(context.Background(), opts, io, refused.Error(), store, &http.Client{Transport: r})
	return out.String(), io, err
}

func wantNothingLeft(t *testing.T, dir, id string, store *memoryStore) {
	t.Helper()
	for key := range store.values {
		if strings.Contains(key, id) {
			t.Errorf("credential left: %s", key)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "revocations"))
	for _, entry := range entries {
		if strings.Contains(entry.Name(), id) {
			t.Errorf("revocation file left: %s", entry.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "profiles", id+".json")); err == nil {
		t.Error("the profile was not removed")
	}
}

func wantNoToken(t *testing.T, sent []string) {
	t.Helper()
	joined := strings.Join(sent, "\n")
	if strings.Contains(joined, refusedAccess) || strings.Contains(joined, refusedRefresh) {
		t.Errorf("a token was sent: %v", sent)
	}
}

func TestRefusedLogoutMakesNoRequestOutsideProduction(t *testing.T) {
	cases := map[string]struct {
		issuer string
		extra  map[string]string
	}{
		"a non-production resource": {"https://auth.getdurin.com/", map[string]string{"resource": "https://evil.example" + refusedPersonalPath}},
		"a lookalike issuer host":   {"https://auth.getdurin.com.evil.example", nil},
		"userinfo in the issuer":    {"https://auth.getdurin.com@evil.example", nil},
		"an uppercase issuer":       {"HTTPS://AUTH.getdurin.com/", nil},
		"a double slash":            {"https://auth.getdurin.com//", nil},
		"a self-hosted app":         {"https://auth.getdurin.com", map[string]string{"appOrigin": "https://durin.acme.test", "resource": "https://durin.acme.test" + refusedPersonalPath}},
		"a lookalike app":           {"https://auth.getdurin.com", map[string]string{"appOrigin": "https://app.getdurin.com.evil.example"}},
		"a lookalike resource":      {"https://auth.getdurin.com", map[string]string{"resource": "https://mcp.getdurin.com.evil.example" + refusedPersonalPath}},
		"an untrusted issuer":       {"https://evil.example/", nil},
	}
	for name, c := range cases {
		dir, id, store := refusedProfile(t, c.issuer, c.extra)
		seedUnrevoked(t, dir, id, store)
		r := &routes{}
		out, io, err := logoutRefused(t, dir, id, store, r, "logout", "--config-dir", dir)
		if err != nil || len(r.requests()) != 0 || io.ExitCode != 0 {
			t.Errorf("%s: err %v, sent %v, exit %d", name, err, r.requests(), io.ExitCode)
			continue
		}
		if !strings.Contains(out, "Removed local credentials for "+id+". "+untrustedSession) {
			t.Errorf("%s: output %s", name, out)
		}
		if strings.Contains(out, refusedAccess) || strings.Contains(out, refusedRefresh) {
			t.Errorf("%s: printed a token", name)
		}
		wantNothingLeft(t, dir, id, store)
		if _, err := os.Stat(filepath.Join(dir, "active")); err == nil {
			t.Errorf("%s: the active profile still names the removed one", name)
		}
	}
}

func TestRefusedLogoutRevokesOnlyAtAnAdvertisedProductionEndpoint(t *testing.T) {
	const endpoint = "https://auth.getdurin.com/oauth2/revoke"
	dir, id, store := refusedProfile(t, "https://auth.getdurin.com/", nil)
	seedUnrevoked(t, dir, id, store)
	r := metadataRoutes(endpoint, 200)
	out, io, err := logoutRefused(t, dir, id, store, r)
	if err != nil || io.ExitCode != 0 {
		t.Fatalf("%v, exit %d", err, io.ExitCode)
	}
	want := []string{
		"GET https://auth.getdurin.com/.well-known/oauth-authorization-server",
		"POST " + endpoint + " token=" + refusedRefresh + "&token_type_hint=refresh_token&client_id=client_one",
	}
	if got := r.requests(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(out, "The server session was revoked.") || !strings.Contains(out, "Run durin to sign in again.") {
		t.Errorf("output %s", out)
	}
	wantNothingLeft(t, dir, id, store)

	// Without a refresh token, the access token goes with the access-token hint.
	dir, id, store = refusedProfile(t, "https://auth.getdurin.com/", nil)
	store.values["mcp:"+id+":tokens"] = `{"access_token":"` + refusedAccess + `","token_type":"Bearer"}`
	r = metadataRoutes(endpoint, 200)
	if _, _, err := logoutRefused(t, dir, id, store, r); err != nil {
		t.Fatal(err)
	}
	if got := r.requests(); len(got) != 2 || got[1] != "POST "+endpoint+" token="+refusedAccess+"&token_type_hint=access_token&client_id=client_one" {
		t.Errorf("sent %v", got)
	}
}

func TestRefusedLogoutSendsNoTokenWithoutATrustedEndpoint(t *testing.T) {
	cases := map[string]*routes{
		"no advertised revocation": metadataRoutes("", 200),
		"another host":             metadataRoutes("https://evil.example/oauth2/revoke", 200),
		"a lookalike suffix":       metadataRoutes("https://auth.getdurin.com.evil/oauth2/revoke", 200),
		"http":                     metadataRoutes("http://auth.getdurin.com/oauth2/revoke", 200),
		"an explicit port":         metadataRoutes("https://auth.getdurin.com:443/oauth2/revoke", 200),
		"userinfo":                 metadataRoutes("https://user@auth.getdurin.com/oauth2/revoke", 200),
		"a failing revoke":         metadataRoutes("https://auth.getdurin.com/oauth2/revoke", 500),
		"a different discovered issuer": {answer: func(req *http.Request) (*http.Response, error) {
			return jsonAnswer(200, `{"issuer":"https://evil.example","authorization_endpoint":"https://auth.getdurin.com/a","token_endpoint":"https://auth.getdurin.com/t","response_types_supported":["code"],"revocation_endpoint":"https://auth.getdurin.com/oauth2/revoke"}`, req), nil
		}},
		"a failing discovery":   {answer: func(req *http.Request) (*http.Response, error) { return jsonAnswer(500, ``, req), nil }},
		"an unreachable server": {answer: func(req *http.Request) (*http.Response, error) { return nil, errors.New("fetch failed") }},
	}
	for name, r := range cases {
		dir, id, store := refusedProfile(t, "https://auth.getdurin.com/", nil)
		out, io, err := logoutRefused(t, dir, id, store, r)
		if err != nil || io.ExitCode != 0 {
			t.Errorf("%s: %v, exit %d", name, err, io.ExitCode)
			continue
		}
		if name != "a failing revoke" {
			wantNoToken(t, r.requests())
		}
		for _, sent := range r.requests() {
			if strings.Contains(sent, "evil") {
				t.Errorf("%s: contacted %s", name, sent)
			}
		}
		if !strings.Contains(out, "Removed local credentials for "+id+". "+notRevoked) {
			t.Errorf("%s: output %s", name, out)
		}
		wantNothingLeft(t, dir, id, store)
	}
}

func TestRefusedLogoutHoldsTheProfileLockAndASecondOneIsAlreadyLoggedOut(t *testing.T) {
	dir, id, store := refusedProfile(t, "https://auth.getdurin.com/", nil)
	r := metadataRoutes("", 200)
	r.gate = make(chan struct{})
	opts := cliArgs(t, "logout", "--profile", id, "--config-dir", dir)
	_, refused := LoadSelectedProfile(opts)
	done := make(chan string)
	go func() {
		var out bytes.Buffer
		_ = logoutRefusedProfile(context.Background(), opts, &IO{Stdout: &out}, refused.Error(), store, &http.Client{Transport: r})
		done <- out.String()
	}()
	lock := filepath.Join(dir, "locks", id+".lock")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the refused logout did not take the profile lock")
		}
	}
	second := make(chan string)
	go func() {
		var out bytes.Buffer
		io := &IO{Stdout: &out}
		_ = logoutRefusedProfile(context.Background(), opts, io, refused.Error(), store, &http.Client{Transport: r})
		second <- out.String()
	}()
	select {
	case <-second:
		t.Fatal("a second logout ran while the first held the lock")
	case <-time.After(250 * time.Millisecond):
	}
	close(r.gate)
	if out := <-done; !strings.Contains(out, "Removed local credentials") {
		t.Errorf("first logout: %s", out)
	}
	if out := <-second; out != "already logged out\n" {
		t.Errorf("second logout: %q", out)
	}
}
