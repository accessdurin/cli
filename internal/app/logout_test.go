package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
)

// authorizationServer answers discovery with metadata (revocation at /revoke unless endpoint
// says otherwise) and every other request with revoke. It records what it was sent.
type authorizationServer struct {
	mu       sync.Mutex
	sent     []string
	metadata func() (*http.Response, error)
	revoke   func() (*http.Response, error)
}

func answer(status int, body string) func() (*http.Response, error) {
	return func() (*http.Response, error) {
		header := http.Header{"Content-Type": {"application/json"}}
		if status/100 == 3 {
			header.Set("Location", "https://portal.example.test/login")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func metadataWith(endpoint string) func() (*http.Response, error) {
	revocation := ""
	if endpoint != "" {
		revocation = `,"revocation_endpoint":"` + endpoint + `"`
	}
	return answer(200, `{"issuer":"https://app.example.test/","authorization_endpoint":"https://app.example.test/authorize","token_endpoint":"https://app.example.test/token","response_types_supported":["code"]`+revocation+`}`)
}

func (s *authorizationServer) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	s.mu.Lock()
	s.sent = append(s.sent, strings.TrimSpace(req.Method+" "+req.URL.Path+" "+body))
	s.mu.Unlock()
	answer := s.revoke
	if strings.Contains(req.URL.Path, ".well-known") {
		answer = s.metadata
	}
	resp, err := answer()
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func newAuthorizationServer(revoke func() (*http.Response, error)) *authorizationServer {
	return &authorizationServer{metadata: metadataWith("https://app.example.test/revoke"), revoke: revoke}
}

func signedInStore(t *testing.T, profile contracts.CliProfile) *memoryStore {
	store := newMemoryStore()
	signInGrant(t, NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store}), profile.McpIssuer)
	return store
}

func TestSignOutRevokesAndClearsOnlyThisProfile(t *testing.T) {
	profile := mustProfile(t, testScope)
	store := signedInStore(t, profile)
	store.values["mcp:other:tokens"] = "kept"
	server := newAuthorizationServer(answer(200, ""))
	marker := &memoryMarker{}
	result, err := SignOutMcp(context.Background(), SignOutOptions{Profile: profile, Store: store, Marker: marker, Fetch: &http.Client{Transport: server}})
	if err != nil || result != (SignOutResult{RevocationRevoked, "cleared"}) {
		t.Fatalf("result %v, %v", result, err)
	}
	want := []string{
		"GET /.well-known/oauth-authorization-server",
		"POST /revoke token=synthetic-refresh&token_type_hint=refresh_token&client_id=client_one",
		"POST /revoke token=synthetic-access&token_type_hint=access_token&client_id=client_one",
	}
	if strings.Join(server.sent, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent\n%s", strings.Join(server.sent, "\n"))
	}
	if len(store.values) != 1 || marker.pending {
		t.Errorf("left %v, marker pending %v", store.values, marker.pending)
	}
}

func TestSignOutKeepsTheGrantUntilTheServerConfirms(t *testing.T) {
	refused := func() (*http.Response, error) { return nil, errors.New("connection refused") }
	unconfirmed := map[string]*authorizationServer{
		"a busy server":              newAuthorizationServer(answer(503, "")),
		"rate limiting":              newAuthorizationServer(answer(429, "")),
		"a refused connection":       newAuthorizationServer(refused),
		"a rejected request":         newAuthorizationServer(answer(400, `{"error":"unsupported_token_type"}`)),
		"a captive portal redirect":  newAuthorizationServer(answer(302, "")),
		"an HTML page for metadata":  {metadata: answer(200, "<html>Sign in to Wi-Fi</html>")},
		"truncated metadata":         {metadata: answer(200, `{"issuer":"https://app`)},
		"metadata that is not found": {metadata: answer(404, "")},
		"metadata behind a redirect": {metadata: answer(302, "")},
	}
	for name, server := range unconfirmed {
		profile := mustProfile(t, testScope)
		store := signedInStore(t, profile)
		marker := &memoryMarker{}
		spool := filepath.Join(tempDir(t), "spool")
		writeFile(t, filepath.Join(spool, "call.json"), "{}")
		options := SignOutOptions{Profile: profile, Store: store, Marker: marker, Fetch: &http.Client{Transport: server}, Spool: spool}
		result, err := SignOutMcp(context.Background(), options)
		if err != nil || result != (SignOutResult{RevocationFailed, "retained"}) || len(store.values) != 2 || !marker.pending {
			t.Errorf("%s: %v, %v, %d entries, marker %v", name, result, err, len(store.values), marker.pending)
		}
		if _, err := os.Stat(spool); err != nil {
			t.Errorf("%s: an unfinished sign-out keeps the spool: %v", name, err)
		}
		// Forced, it signs out on this computer anyway, and calls queued for the profile go too.
		options.Force = true
		result, err = SignOutMcp(context.Background(), options)
		if err != nil || result != (SignOutResult{RevocationFailed, "cleared"}) || len(store.values) != 0 || marker.pending {
			t.Errorf("%s forced: %v, %v, %v, marker %v", name, result, err, store.values, marker.pending)
		}
		if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s forced: spool left: %v", name, err)
		}
	}
}

func TestSignOutClearsGrantsThatCanNeverBeRevoked(t *testing.T) {
	cases := map[string]struct {
		server *authorizationServer
		store  func(*memoryStore, string)
		want   RevocationStatus
	}{
		// Only the server's own metadata can say that it offers no revocation.
		"no revocation endpoint": {server: &authorizationServer{metadata: metadataWith("")}, want: RevocationUnavailable},
		"nothing stored":         {server: newAuthorizationServer(answer(200, "")), store: func(s *memoryStore, id string) { clear(s.values) }, want: RevocationUnavailable},
		"unreadable credentials": {server: newAuthorizationServer(answer(500, "")), store: func(s *memoryStore, id string) { s.values["mcp:"+id+":tokens"] = "not json" }, want: RevocationFailed},
		// An endpoint off the profile's hosts or without HTTPS can never be used.
		"an unsafe endpoint":   {server: &authorizationServer{metadata: metadataWith("http://app.example.test/revoke"), revoke: answer(200, "")}, want: RevocationFailed},
		"an unlisted endpoint": {server: &authorizationServer{metadata: metadataWith("https://evil.example.test/revoke"), revoke: answer(200, "")}, want: RevocationFailed},
	}
	for name, c := range cases {
		profile := mustProfile(t, testScope)
		store := signedInStore(t, profile)
		if c.store != nil {
			c.store(store, profile.ID)
		}
		result, err := SignOutMcp(context.Background(), SignOutOptions{Profile: profile, Store: store, Marker: &memoryMarker{}, Fetch: &http.Client{Transport: c.server}})
		if err != nil || result != (SignOutResult{c.want, "cleared"}) || len(store.values) != 0 {
			t.Errorf("%s: %v, %v, %v", name, result, err, store.values)
		}
		for _, sent := range c.server.sent {
			if strings.HasPrefix(sent, "POST") {
				t.Errorf("%s: sent %s", name, sent)
			}
		}
	}
}

func TestPendingRevocationsAreRetriedWithoutWaiting(t *testing.T) {
	ctx := context.Background()
	dir := tempDir(t)
	profile := mustProfile(t, testScope)
	store := newMemoryStore()
	pending, err := NewFilePendingRevocations(dir, profile.ID, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Add(contracts.UnrevokedGrant{ClientID: "client-old", AccessToken: "old-access"}); err != nil {
		t.Fatal(err)
	}
	failing := &http.Client{Transport: newAuthorizationServer(answer(500, ""))}
	if err := RetryPendingRevocations(ctx, dir, profile, store, failing); err != nil {
		t.Fatal(err)
	}
	if entries, _ := pending.Entries(); len(entries) != 1 {
		t.Fatalf("forgot an unrevoked grant: %v", entries)
	}

	// A busy profile is skipped at once, not waited for.
	server := newAuthorizationServer(answer(200, ""))
	lock := filepath.Join(dir, "locks", profile.ID+".lock")
	writeFile(t, lock, `{"pid":`+strconv.Itoa(os.Getpid())+`,"nonce":"6f1c2b9e-0d4a-4c3b-9a7e-2f5d8c1b0a99"}`)
	start := time.Now()
	if err := RetryPendingRevocations(ctx, dir, profile, store, &http.Client{Transport: server}); err != nil || time.Since(start) > time.Second || len(server.sent) != 0 {
		t.Fatalf("busy profile: %v after %v, sent %v", err, time.Since(start), server.sent)
	}
	_ = os.Remove(lock)

	if err := RetryPendingRevocations(ctx, dir, profile, store, &http.Client{Transport: server}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := pending.Entries(); len(entries) != 0 || len(store.values) != 0 {
		t.Fatalf("kept a revoked grant: %v, %v", entries, store.values)
	}

	// Retiring a kept grant hands it to the pending list when the server cannot confirm.
	signInGrant(t, NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: store}), profile.McpIssuer)
	if err := RetireKeptGrant(ctx, RetireOptions{Profile: profile, Store: store, Pending: pending, Fetch: failing}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := pending.Entries(); len(entries) != 1 || entries[0].RefreshToken != "synthetic-refresh" {
		t.Fatalf("pending %v", entries)
	}
	if _, found, _ := store.Read("mcp:" + profile.ID + ":tokens"); found {
		t.Fatal("the retired grant is still usable")
	}
}
