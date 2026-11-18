package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
)

func TestFailuresUseTheUnavailableMessage(t *testing.T) {
	store := NewNative("com.getdurin.cli.test")
	invalid := "\xff"
	_, _, readErr := store.Read(invalid)
	errs := []error{readErr, store.Write("key", invalid), store.Remove(invalid)}
	for i, err := range errs {
		if err == nil || err.Error() != UnavailableMessage || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("operation %d: got %v, want ErrUnavailable", i, err)
		}
		if errors.Unwrap(err) == nil {
			t.Fatalf("operation %d: cause was dropped", i)
		}
	}
	if UnavailableMessage != "OS credential store unavailable. Unlock your credential store and run durin again." {
		t.Fatal("message differs from the TypeScript CLI")
	}
}

func TestUTF16LE(t *testing.T) {
	for _, value := range []string{"", "token", "é ✓ 密钥 🔑"} {
		decoded, err := decodeUTF16LE(encodeUTF16LE(value))
		if err != nil || decoded != value {
			t.Fatalf("round trip of %q: got %q, %v", value, decoded, err)
		}
	}
	// "a🔑" is 61 00 3D D8 11 DD: no terminating NUL, surrogate pair kept.
	if got := hex.EncodeToString(encodeUTF16LE("a🔑")); got != "61003dd811dd" {
		t.Fatalf("encoding: got %s", got)
	}
	for _, bad := range [][]byte{
		{0x61},                   // odd length
		{0x3d, 0xd8},             // lone high surrogate
		{0x11, 0xdd, 0x61, 0x00}, // lone low surrogate
		{0x3d, 0xd8, 0x61, 0x00}, // high surrogate followed by a non-surrogate
	} {
		if _, err := decodeUTF16LE(bad); err == nil {
			t.Fatalf("decoding % x: want an error", bad)
		}
	}
}

// keyringTest skips unless DURIN_TEST_KEYRING=1, and returns a random service
// name so tests never touch real entries.
func keyringTest(t *testing.T) string {
	t.Helper()
	if os.Getenv("DURIN_TEST_KEYRING") != "1" {
		t.Skip("set DURIN_TEST_KEYRING=1 to run against the real OS credential store")
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return "com.getdurin.cli.test." + hex.EncodeToString(suffix)
}

func TestNativeRoundTrip(t *testing.T) {
	store := NewNative(keyringTest(t))
	const key = "mcp:example:tokens"

	expectMissing := func() {
		t.Helper()
		if value, found, err := store.Read(key); err != nil || found || value != "" {
			t.Fatalf("read of a missing entry: got %q, %v, %v", value, found, err)
		}
		if err := store.Remove(key); err != nil {
			t.Fatalf("remove of a missing entry: %v", err)
		}
	}
	expectValue := func(want string) {
		t.Helper()
		if value, found, err := store.Read(key); err != nil || !found || value != want {
			t.Fatalf("read: got %q, %v, %v; want %q", value, found, err, want)
		}
	}

	expectMissing()
	for _, value := range []string{`{"access_token":"a"}`, "é ✓ 密钥 🔑", ""} {
		if err := store.Write(key, value); err != nil {
			t.Fatalf("write %q: %v", value, err)
		}
		expectValue(value)
	}
	if err := store.Remove(key); err != nil {
		t.Fatalf("remove: %v", err)
	}
	expectMissing()
}
