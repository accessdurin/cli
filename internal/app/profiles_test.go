package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

var testScope = contracts.CliProfileScope{
	AppOrigin: "https://app.example.test", AccountIssuer: "https://app.example.test/", AccountClientID: "client_account",
	PrincipalID: "user_one", WorkosOrganizationID: "org_provider_one", OrganizationID: "org_one", Environment: "production",
	Resource: "https://app.example.test/mcp/u/" + strings.Repeat("a", 64), McpIssuer: "https://app.example.test/",
}

func mustProfile(t *testing.T, scope contracts.CliProfileScope) contracts.CliProfile {
	t.Helper()
	profile, err := CreateProfile(scope)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func TestProfilesStayPinnedAndRejectTamperedBindings(t *testing.T) {
	dir := tempDir(t)
	profiles := NewFileProfiles(dir)
	first := mustProfile(t, testScope)
	secondScope := testScope
	secondScope.OrganizationID = "org_two"
	second := mustProfile(t, secondScope)
	for _, p := range []contracts.CliProfile{first, second} {
		if err := profiles.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, active := range []contracts.CliProfile{first, second} {
		if err := profiles.Activate(active.ID); err != nil {
			t.Fatal(err)
		}
		if loaded, err := profiles.Load(""); err != nil || loaded != active {
			t.Fatalf("active profile: %+v %v", loaded, err)
		}
	}
	if loaded, err := profiles.Load(first.ID); err != nil || loaded != first {
		t.Fatalf("explicit profile: %v", err)
	}
	path := filepath.Join(dir, "profiles", first.ID+".json")
	if got, want := readFile(t, path), jsjson.StringifyIndent(first.JSON(), 2)+"\n"; got != want {
		t.Fatalf("saved file:\n%s\nwant\n%s", got, want)
	}
	for _, field := range []string{"principalId", "organizationId", "mcpIssuer", "resource"} {
		tampered := first.JSON()
		value, _ := tampered.GetString(field)
		tampered.Set(field, jsjson.String(value+"-changed"))
		writeFile(t, path, jsjson.Stringify(tampered))
		_, err := profiles.Load(first.ID)
		wantCliError(t, err, "profile binding")
	}
}

func TestProfilesRefuseUnusedHostsEvenWithAnIntactBinding(t *testing.T) {
	dir := tempDir(t)
	scope := testScope
	scope.AppOrigin, scope.AccountIssuer = "https://app.getdurin.com", "https://auth.getdurin.com/"
	scope.Resource = "https://evil-durin.com/mcp/u/" + strings.Repeat("a", 64)
	scope.McpIssuer = "https://auth.getdurin.com/"
	id := contracts.CliProfile{CliProfileScope: scope}.JSON()
	id.Delete("version")
	id.Delete("id")
	sum := textDigest(jsjson.Stringify(id))
	writeFile(t, filepath.Join(dir, "profiles", sum+".json"), jsjson.Stringify(contracts.CliProfile{CliProfileScope: scope, Version: 1, ID: sum}.JSON()))
	writeFile(t, filepath.Join(dir, "active"), sum+"\n")
	profiles := NewFileProfiles(dir)
	_, err := profiles.Load("")
	wantCliError(t, err, "The MCP endpoint is on evil-durin.com, but the durin app at app.getdurin.com uses mcp.getdurin.com or app.getdurin.com. Run durin to sign in again.")
	_, err = profiles.List()
	wantCliError(t, err, "evil-durin.com")
}

func TestProfileLockSerializesAndReleases(t *testing.T) {
	dir := tempDir(t)
	id := strings.Repeat("a", 64)
	refresh := "initial"
	var seen []string
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := WithProfileLock(dir, id, func() (struct{}, error) {
				mu.Lock()
				seen = append(seen, refresh)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				refresh += "-rotated"
				mu.Unlock()
				return struct{}{}, nil
			}, DefaultLockPatience)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if strings.Join(seen, ",") != "initial,initial-rotated" {
		t.Fatalf("not serialized: %v", seen)
	}
	if _, err := WithProfileLock(dir, id, func() (int, error) { return 0, os.ErrInvalid }, DefaultLockPatience); err != os.ErrInvalid {
		t.Fatalf("work error: %v", err)
	}
	if got, err := WithProfileLock(dir, id, func() (string, error) { return "released", nil }, DefaultLockPatience); err != nil || got != "released" {
		t.Fatalf("lock not released: %v", err)
	}

	lock := filepath.Join(dir, "locks", id+".lock")
	writeFile(t, lock, `{"pid":`+jsjson.FormatNumber(float64(os.Getpid()))+`,"nonce":"10000000-0000-4000-8000-000000000001"}`)
	_, err := WithProfileLock(dir, id, func() (int, error) { return 0, nil }, 150*time.Millisecond)
	wantCliError(t, err, "busy in another Durin process")
	// An abandoned lock is reported, never removed automatically.
	writeFile(t, lock, `{"pid":2147483647,"nonce":"10000000-0000-4000-8000-000000000001"}`)
	_, err = WithProfileLock(dir, id, func() (int, error) { return 0, nil }, DefaultLockPatience)
	wantCliError(t, err, "interrupted")
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("the abandoned lock was removed")
	}
}
