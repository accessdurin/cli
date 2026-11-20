package credentials

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// tsKeyring calls the TypeScript CLI's credential library through
// testdata/napi-keyring.mjs. See that file for how to run this test.
type tsKeyring struct {
	t       *testing.T
	service string
}

type tsResponse struct {
	Value   *string `json:"value"`
	Deleted *bool   `json:"deleted"`
	Error   string  `json:"error"`
}

func (k tsKeyring) call(op, key, value string) tsResponse {
	k.t.Helper()
	request, err := json.Marshal(map[string]string{"op": op, "service": k.service, "key": key, "value": value})
	if err != nil {
		k.t.Fatal(err)
	}
	cmd := exec.Command("node", "testdata/napi-keyring.mjs")
	cmd.Stdin = bytes.NewReader(request)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		k.t.Fatalf("node %s: %v\n%s", op, err, stderr.String())
	}
	var response tsResponse
	if err := json.Unmarshal(out, &response); err != nil {
		k.t.Fatalf("node %s: %v: %s", op, err, out)
	}
	if response.Error != "" {
		k.t.Fatalf("TypeScript %s rejected: %s", op, response.Error)
	}
	return response
}

func TestCompatWithTypeScript(t *testing.T) {
	service := keyringTest(t)
	if os.Getenv("DURIN_TS_KEYRING") == "" {
		t.Skip("set DURIN_TS_KEYRING to the CLI's node_modules/@napi-rs/keyring/index.js")
	}
	ts := tsKeyring{t: t, service: service}
	store := NewNative(service)
	const tsKey, goKey = "mcp:written-by-ts:tokens", "mcp:written-by-go:tokens"
	value := `{"access_token":"é ✓ 密钥 🔑","refresh_token":"r"}`

	// TypeScript writes, Go reads, overwrites and removes.
	ts.call("set", tsKey, value)
	if got, found, err := store.Read(tsKey); err != nil || !found || got != value {
		t.Fatalf("Go read of a TypeScript entry: got %q, %v, %v", got, found, err)
	}

	// Go writes, TypeScript reads.
	if err := store.Write(goKey, value); err != nil {
		t.Fatal(err)
	}
	if got := ts.call("get", goKey, ""); got.Value == nil || *got.Value != value {
		t.Fatalf("TypeScript read of a Go entry: got %v", got.Value)
	}

	// Both writers produced the same native entry.
	if runtime.GOOS == "linux" {
		compareSecretServiceItems(t, service, tsKey, goKey)
	}

	// Updates go to the existing entry in both directions.
	if err := store.Write(tsKey, "updated by go"); err != nil {
		t.Fatal(err)
	}
	if got := ts.call("get", tsKey, ""); got.Value == nil || *got.Value != "updated by go" {
		t.Fatalf("TypeScript read after a Go update: got %v", got.Value)
	}
	ts.call("set", goKey, "updated by ts")
	if got, found, err := store.Read(goKey); err != nil || !found || got != "updated by ts" {
		t.Fatalf("Go read after a TypeScript update: got %q, %v, %v", got, found, err)
	}

	// Removal is seen by the other side, and removing again is not an error.
	if err := store.Remove(tsKey); err != nil {
		t.Fatal(err)
	}
	if got := ts.call("get", tsKey, ""); got.Value != nil {
		t.Fatalf("TypeScript still reads a Go-removed entry: %q", *got.Value)
	}
	if got := ts.call("delete", goKey, ""); got.Deleted == nil || !*got.Deleted {
		t.Fatal("TypeScript did not delete the Go entry")
	}
	if got := ts.call("delete", goKey, ""); got.Deleted == nil || *got.Deleted {
		t.Fatal("TypeScript delete of a missing entry should resolve false")
	}
	if _, found, err := store.Read(goKey); err != nil || found {
		t.Fatalf("Go still reads a TypeScript-removed entry: %v, %v", found, err)
	}
}
