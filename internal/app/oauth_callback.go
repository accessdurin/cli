package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
)

// AuthorizationCode is what the authorization server returned to the loopback callback.
// Issuer is empty when the callback carried no iss.
type AuthorizationCode struct {
	Code   string
	Issuer string
}

var callbackMessages = map[string]string{
	"code":   "Durin authorization received. Return to your terminal to finish setup.",
	"denied": "Durin authorization was denied. Return to your terminal.",
}

func reply(w http.ResponseWriter, status int, message string) {
	header := w.Header()
	header.Set("content-type", "text/plain; charset=utf-8")
	header.Set("cache-control", "no-store")
	header.Set("referrer-policy", "no-referrer")
	header.Set("connection", "close")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message))
}

// LoopbackAuthorization receives one OAuth authorization response on
// http://127.0.0.1:<port>/oauth/callback (src/oauth-callback.ts). It accepts only a callback
// with this transaction's State, answers it, and stops listening.
type LoopbackAuthorization struct {
	// State is 32 random bytes, base64url without padding.
	State string
	// RedirectURL is set once listening.
	RedirectURL string

	issuer   string
	server   *http.Server
	listener net.Listener

	mu      sync.Mutex
	used    bool
	timer   *time.Timer
	settled bool
	done    chan struct{}
	result  AuthorizationCode
	err     error
}

func newLoopbackAuthorization(issuer string) *LoopbackAuthorization {
	var state [32]byte
	if _, err := rand.Read(state[:]); err != nil {
		panic(err)
	}
	l := &LoopbackAuthorization{State: base64.RawURLEncoding.EncodeToString(state[:]), issuer: issuer, done: make(chan struct{})}
	l.server = &http.Server{Handler: http.HandlerFunc(l.accept), MaxHeaderBytes: 8192, ReadHeaderTimeout: 30 * time.Second}
	return l
}

// settle resolves or rejects the result once; later calls do nothing.
func (l *LoopbackAuthorization) settle(code AuthorizationCode, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.settled {
		return
	}
	l.settled, l.result, l.err = true, code, err
	close(l.done)
}

func (l *LoopbackAuthorization) listen(timeout time.Duration) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		listener.Close()
		return clierror.New("Cannot listen for MCP authorization.")
	}
	l.listener = listener
	l.RedirectURL = "http://127.0.0.1:" + strconv.Itoa(address.Port) + "/oauth/callback"
	go func() { _ = l.server.Serve(listener) }()
	l.mu.Lock()
	l.timer = time.AfterFunc(timeout, func() {
		l.settle(AuthorizationCode{}, clierror.New("MCP authorization expired. Run durin to sign in again."))
		_ = l.Close()
	})
	l.mu.Unlock()
	return nil
}

// Result waits for the authorization code. It fails when the user denies authorization, the
// wait times out, the callback is closed first, or ctx ends.
func (l *LoopbackAuthorization) Result(ctx context.Context) (AuthorizationCode, error) {
	select {
	case <-l.done:
		return l.result, l.err
	case <-ctx.Done():
		return AuthorizationCode{}, ctx.Err()
	}
}

// Close stops listening and drops open connections. A result not yet received fails as
// interrupted.
func (l *LoopbackAuthorization) Close() error {
	l.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
	}
	l.mu.Unlock()
	l.settle(AuthorizationCode{}, clierror.New("MCP authorization interrupted. Run durin to sign in again."))
	if err := l.server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

func (l *LoopbackAuthorization) accept(w http.ResponseWriter, r *http.Request) {
	input := callbackInput(r, callbackBinding{RedirectURL: l.RedirectURL, State: l.State, Issuer: l.issuer})
	if input == nil {
		reply(w, http.StatusBadRequest, "Invalid authorization callback.")
		return
	}
	l.mu.Lock()
	used := l.used
	l.used = true
	if !used && l.timer != nil {
		l.timer.Stop()
	}
	l.mu.Unlock()
	if used {
		reply(w, http.StatusGone, "Authorization already received.")
		return
	}
	reply(w, http.StatusOK, callbackMessages[input.Kind])
	if input.Kind == "denied" {
		l.settle(AuthorizationCode{}, clierror.New("MCP authorization was denied."))
	} else {
		l.settle(AuthorizationCode{Code: input.Code, Issuer: input.Issuer}, nil)
	}
	// Stop accepting connections; this response still completes.
	_ = l.listener.Close()
}

// ListenForAuthorization starts the loopback callback for an authorization by issuer. It
// stops waiting after timeout (300 seconds when 0).
func ListenForAuthorization(issuer string, timeout time.Duration) (*LoopbackAuthorization, error) {
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	callback := newLoopbackAuthorization(issuer)
	if err := callback.listen(timeout); err != nil {
		return nil, err
	}
	return callback, nil
}
