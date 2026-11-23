package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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

// The gateway sees the agent's messages in the agent's order, one at a time, even under a
// lock whose waiters are not served first come, first served (the profile lock polls).
func TestBridgeForwardsInTheAgentsOrder(t *testing.T) {
	profile := mustProfile(t, testScope)
	input, agent := io.Pipe()
	var mu sync.Mutex
	var sent []string
	posted := make(chan struct{}, 8)
	inside, overlapped := 0, false
	another := make(chan struct{}, 8)
	marker := &lockedMarker{}
	bridge, err := NewMcpStdioBridge(McpStdioBridgeOptions{
		Resource:      profile.Resource,
		Authorization: NewMcpOAuthProvider(McpOAuthOptions{Profile: profile, Store: newMemoryStore(), Marker: marker}),
		Fetch: func(_ context.Context, _ string, init *httpx.RequestInit) (*http.Response, error) {
			if init.Method == http.MethodPost {
				mu.Lock()
				sent = append(sent, string(init.Body))
				mu.Unlock()
				posted <- struct{}{}
			}
			return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		},
		Input:  input,
		Output: &bytes.Buffer{},
		Marker: marker,
		// The first holder waits long enough for any forward started beside it to arrive.
		Serialize: func(work func() error) error {
			mu.Lock()
			inside++
			first := len(sent) == 0 && inside == 1
			if inside > 1 {
				overlapped = true
				another <- struct{}{}
			}
			mu.Unlock()
			if first {
				select {
				case <-another:
				case <-time.After(200 * time.Millisecond):
				}
			}
			defer func() { mu.Lock(); inside--; mu.Unlock() }()
			return work()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"jsonrpc":"2.0","method":"notifications/first"}`,
		`{"jsonrpc":"2.0","method":"notifications/second"}`,
		`{"jsonrpc":"2.0","method":"notifications/third"}`,
	}
	for _, line := range want {
		if _, err := io.WriteString(agent, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	for range want {
		select {
		case <-posted:
		case <-time.After(5 * time.Second):
			t.Fatal("the bridge did not forward every message")
		}
	}
	_ = agent.Close()
	bridge.Wait()
	mu.Lock()
	defer mu.Unlock()
	if overlapped {
		t.Fatal("a forward waited for the profile lock while an earlier one held it, so the lock's polling decides the order the gateway sees")
	}
	if strings.Join(sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forwarded %q, want %q", sent, want)
	}
}
