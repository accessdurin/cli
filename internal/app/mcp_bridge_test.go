package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
)

type lockedMarker struct {
	mu      sync.Mutex
	pending bool
}

func (m *lockedMarker) Pending() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending, nil
}
func (m *lockedMarker) Mark() error      { return nil }
func (m *lockedMarker) Clear() error     { return nil }
func (m *lockedMarker) set(pending bool) { m.mu.Lock(); m.pending = pending; m.mu.Unlock() }

func TestBridgeClosesWhenAnotherProcessLeavesSignOutUnfinished(t *testing.T) {
	profile := mustProfile(t, testScope)
	input, _ := io.Pipe()
	var reports []string
	var mu sync.Mutex
	marker := &lockedMarker{pending: true}
	bridge, err := NewMcpStdioBridge(McpStdioBridgeOptions{
		Resource:      profile.Resource,
		Authorization: NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: newMemoryStore(), Marker: marker}),
		Fetch: func(context.Context, string, *httpx.RequestInit) (*http.Response, error) {
			t.Error("the bridge sent a request")
			return nil, errors.New("unexpected")
		},
		Input:   input,
		Output:  &bytes.Buffer{},
		Marker:  marker,
		OnError: func(message string) { mu.Lock(); reports = append(reports, message); mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A bridge that starts while sign-out is unfinished stays open to explain each refusal.
	if err := bridge.closeOnSignOut(); err != nil || bridge.isClosed() {
		t.Fatalf("closed a bridge started during sign-out: %v", err)
	}
	marker.set(false)
	_ = bridge.closeOnSignOut()
	marker.set(true)
	_ = bridge.closeOnSignOut()
	<-bridge.Done()
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || reports[0] != SignOutUnfinished {
		t.Fatalf("reported %q", reports)
	}

	if _, err := NewMcpStdioBridge(McpStdioBridgeOptions{Resource: "http://app.example.test/mcp"}); err == nil {
		t.Fatal("bridged an unsafe resource")
	}
}

func TestBridgeFailureMessages(t *testing.T) {
	for err, want := range map[error]string{
		clierror.New(SignOutUnfinished):              SignOutUnfinished,
		oauth.NewUnauthorizedError():                 SignInAgain,
		errors.New("upstream detail \x1b[31m"):       bridgeRequestFailed,
		&httpx.TypeError{Err: errors.New("refused")}: bridgeRequestFailed,
	} {
		if got := bridgeFailureMessage(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}
