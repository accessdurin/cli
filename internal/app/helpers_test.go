package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/accessdurin/cli/internal/clierror"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// wantCliError fails unless err is an expected CLI error whose message contains part.
func wantCliError(t *testing.T, err error, part string) {
	t.Helper()
	e, ok := clierror.As(err)
	if !ok || !strings.Contains(e.Message, part) {
		t.Fatalf("want a CLI error containing %q, got %v", part, err)
	}
}

// memoryStore is a ports.CredentialStore in memory; failWrite makes writes fail.
type memoryStore struct {
	mu        sync.Mutex
	values    map[string]string
	failWrite bool
}

func newMemoryStore() *memoryStore { return &memoryStore{values: map[string]string{}} }

func (m *memoryStore) Read(key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	return v, ok, nil
}

func (m *memoryStore) Write(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWrite {
		return errors.New("credential store unavailable")
	}
	m.values[key] = value
	return nil
}

func (m *memoryStore) Remove(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}
