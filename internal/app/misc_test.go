package app

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

func TestBundledSkillsAreByteExact(t *testing.T) {
	for _, skill := range bundledSkills {
		if textDigest(skill.Content) != skill.SHA256 {
			t.Errorf("%s differs from the reviewed skills revision", skill.Name)
		}
	}
}

func TestCallLedgerExpiresAndKeepsOnlyIdentifiers(t *testing.T) {
	dir := tempDir(t)
	now := time.Now()
	ledger, _ := NewFileCallLedger(dir, testProfileID, func() time.Time { return now })
	fingerprint := strings.Repeat("f", 64)
	if call, err := ledger.Read(fingerprint); err != nil || call != nil {
		t.Fatalf("empty ledger: %+v %v", call, err)
	}
	if err := ledger.Save(fingerprint, contracts.PendingCliCall{IdempotencyKey: "cli_1", ApprovalID: "appr_1"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "calls", testProfileID, fingerprint+".json")
	if got := readFile(t, path); got != `{"idempotencyKey":"cli_1","approvalId":"appr_1"}` {
		t.Fatalf("saved %s", got)
	}
	if call, err := ledger.Read(fingerprint); err != nil || *call != (contracts.PendingCliCall{IdempotencyKey: "cli_1", ApprovalID: "appr_1"}) {
		t.Fatalf("read %+v %v", call, err)
	}
	now = time.Now().Add(CallRetention)
	if call, err := ledger.Read(fingerprint); err != nil || call != nil {
		t.Fatalf("expired call resumed: %+v %v", call, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired call kept")
	}
	if _, err := ledger.Read("not-a-fingerprint"); err == nil {
		t.Fatal("accepted an invalid fingerprint")
	}
}

type refuseTransport struct{ t *testing.T }

func (r refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Fatalf("sent a request to %s", req.URL)
	return nil, nil
}

type okTransport struct{ status int }

func (o okTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	header := http.Header{}
	if o.status/100 == 3 {
		header.Set("Location", "https://issuer.example.test/next")
	}
	return &http.Response{StatusCode: o.status, Header: header, Body: http.NoBody, Request: req}, nil
}

func TestSecureFetch(t *testing.T) {
	allowlist := contracts.CliHostAllowlist{Hosts: []string{"issuer.example.test"}}
	refusing := SecureFetch(allowlist, &http.Client{Transport: refuseTransport{t}})
	for _, u := range []string{"http://issuer.example.test/token", "https://user:secret@issuer.example.test/token"} {
		_, err := refusing.Post(u, "text/plain", strings.NewReader("secret"))
		wantCliError(t, err, "Unsafe authorization or MCP destination.")
	}
	for _, u := range []string{"https://evil.example/token", "https://issuer.example.test.evil.example/token", "https://evil-issuer.example.test/token",
		"https://attacker.issuer.example.test/token", "https://issuer.example.test:8443/token", "https://localhost/token", "https://127.0.0.1/token"} {
		_, err := refusing.Post(u, "text/plain", strings.NewReader("secret"))
		parsed, _ := url.Parse(u)
		wantCliError(t, err, "durin didn’t contact "+parsed.Host)
	}
	_, err := refusing.Get("https://evil.example/revoke")
	wantCliError(t, err, "It isn’t one of the hosts this durin deployment uses (issuer.example.test). Nothing was sent to it.")

	ok := SecureFetch(allowlist, &http.Client{Transport: okTransport{200}})
	for _, u := range []string{"https://ISSUER.Example.test/token", "https://issuer.example.test:443/token"} {
		if res, err := ok.Get(u); err != nil || res.StatusCode != 200 {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if ok.Timeout != RequestTimeout {
		t.Fatal("no timeout")
	}
	_, err = SecureFetch(contracts.CliHostAllowlist{Hosts: []string{"127.0.0.1"}}, &http.Client{Transport: okTransport{200}}).Get("https://127.0.0.1/token")
	wantCliError(t, err, "--dev")
	if res, err := SecureFetch(contracts.CliHostAllowlist{Development: true}, &http.Client{Transport: okTransport{200}}).Get("http://127.0.0.1:5272/token"); err != nil || res.StatusCode != 200 {
		t.Fatalf("development loopback: %v", err)
	}
	if _, err := SecureFetch(allowlist, &http.Client{Transport: okTransport{302}}).Get("https://issuer.example.test/token"); !errors.Is(err, ErrRedirect) {
		t.Fatalf("redirect followed: %v", err)
	}
}

var controlChars = regexp.MustCompile(`[\p{Cc}\x{061c}\x{200e}\x{200f}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)

func TestTerminalText(t *testing.T) {
	for _, value := range []string{
		"Acme\x1b]52;c;ZWNobyBwd25lZA==\x07 Corp", "Acme\x1b]52;c;a+b/c==\x07 Corp", "Acme\x1b[1A\x1b[2K Corp",
		"Acme\x1b]8;;https://attacker.example.test\x07 Corp\x1b]8;;\x07", "Acme\u009b2J Corp", "Acme‮ Corp", "Acme\r\n Corp",
	} {
		rendered := TerminalText(value)
		if controlChars.MatchString(rendered) || !strings.HasPrefix(rendered, "Acme") || !strings.HasSuffix(rendered, " Corp") {
			t.Errorf("%q rendered %q", value, rendered)
		}
	}
	international := "Café Ünïcode 株式会社 שלום 👩‍💻"
	if TerminalText(international) != international {
		t.Error("changed ordinary international text")
	}
	value := jsjson.Obj(jsjson.KV("text", jsjson.String("a\u001b[2Jb\u009b2Jc\u007fd\u0085e‮f⁦g")), jsjson.KV("name", jsjson.String("Café")))
	printed := jsjson.TerminalJSON(value)
	parsed, err := jsjson.ParseString(printed)
	if controlChars.MatchString(strings.ReplaceAll(printed, "\n", "")) || err != nil || !jsjson.Equal(parsed, value) {
		t.Errorf("terminal JSON %q", printed)
	}
}
