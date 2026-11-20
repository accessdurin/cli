//go:build linux

package credentials

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestNoSessionBusIsUnavailable(t *testing.T) {
	// The TypeScript CLI's libdbus only uses DBUS_SESSION_BUS_ADDRESS: no
	// $XDG_RUNTIME_DIR/bus fallback and no autolaunch.
	for _, address := range []string{"", "autolaunch:"} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
		store := NewNative("com.getdurin.cli.test")
		_, _, readErr := store.Read("key")
		for i, err := range []error{readErr, store.Write("key", "value"), store.Remove("key")} {
			if !errors.Is(err, ErrUnavailable) || err.Error() != UnavailableMessage {
				t.Fatalf("%q, operation %d: got %v, want ErrUnavailable", address, i, err)
			}
		}
	}
}

// secretToolItem is one item from `secret-tool search --all`, without its
// secret and timestamps.
type secretToolItem struct {
	Label      string
	Attributes map[string]string
}

// compareSecretServiceItems checks that the TypeScript-written and the
// Go-written item have the same label format, attributes, collection and
// content type.
func compareSecretServiceItems(t *testing.T, service, tsKey, goKey string) {
	t.Helper()
	out, err := exec.Command("secret-tool", "search", "--all", "service", service).CombinedOutput()
	if err != nil {
		t.Fatalf("secret-tool: %v\n%s", err, out)
	}
	items := map[string]secretToolItem{}
	var current *secretToolItem
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "[") {
			current = &secretToolItem{Attributes: map[string]string{}}
			continue
		}
		name, value, ok := strings.Cut(line, " = ")
		if !ok || current == nil {
			continue
		}
		switch {
		case name == "label":
			current.Label = value
		case strings.HasPrefix(name, "attribute."):
			current.Attributes[strings.TrimPrefix(name, "attribute.")] = value
			if name == "attribute.username" {
				items[value] = *current
			}
		}
	}
	if len(items) != 2 {
		t.Fatalf("secret-tool found %d items, want 2:\n%s", len(items), redactSecrets(string(out)))
	}
	for _, key := range []string{tsKey, goKey} {
		want := secretToolItem{
			Label:      "keyring:" + key + "@" + service,
			Attributes: map[string]string{"service": service, "username": key},
		}
		if got := items[key]; !reflect.DeepEqual(got, want) {
			t.Errorf("item for %s: got %+v, want %+v", key, got, want)
		}
	}
	t.Logf("secret-tool search --all service %s:\n%s", service, redactSecrets(string(out)))

	// The collection and content type are not shown by secret-tool. (Both
	// writers send application/octet-stream, but gnome-keyring reports
	// text/plain for every item, so only equality is checked.)
	s, err := openSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	collection, err := s.defaultCollection()
	if err != nil {
		t.Fatal(err)
	}
	contentTypes := map[string]string{}
	for _, key := range []string{tsKey, goKey} {
		item, found, err := s.uniqueItem(service, key)
		if err != nil || !found {
			t.Fatalf("item for %s: %v, %v", key, found, err)
		}
		if !strings.HasPrefix(string(item), string(collection)+"/") {
			t.Errorf("item for %s is %s, not in the default collection %s", key, item, collection)
		}
		var secret ssSecret
		if err := s.object(item).Call(ssItem+".GetSecret", 0, s.path).Store(&secret); err != nil {
			t.Fatal(err)
		}
		contentTypes[key] = secret.ContentType
	}
	if contentTypes[tsKey] != contentTypes[goKey] {
		t.Errorf("content types differ: %v", contentTypes)
	}
}

func redactSecrets(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "secret = ") {
			lines[i] = "secret = <redacted>"
		}
	}
	return strings.Join(lines, "\n")
}
