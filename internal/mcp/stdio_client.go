package mcp

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultInheritedEnvVars is DEFAULT_INHERITED_ENV_VARS for the current platform.
var DefaultInheritedEnvVars = func() []string {
	if runtime.GOOS == "windows" {
		return []string{"APPDATA", "COMSPEC", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA", "PATH", "PATHEXT",
			"PROCESSOR_ARCHITECTURE", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432",
			"SYSTEMDRIVE", "SYSTEMROOT", "TEMP", "USERNAME", "USERPROFILE", "WINDIR"}
	}
	return []string{"HOME", "LOGNAME", "PATH", "SHELL", "TERM", "USER"}
}()

// GetDefaultEnvironment is getDefaultEnvironment: the inherited variables from
// DefaultInheritedEnvVars that are set, skipping values that start with "()" (exported shell
// functions).
func GetDefaultEnvironment() map[string]string {
	env := map[string]string{}
	for _, key := range DefaultInheritedEnvVars {
		value, ok := lookupEnv(key)
		if !ok || strings.HasPrefix(value, "()") {
			continue
		}
		env[key] = value
	}
	return env
}

// lookupEnv reads process.env[key]; on Windows process.env is case-insensitive.
func lookupEnv(key string) (string, bool) {
	if runtime.GOOS != "windows" {
		return os.LookupEnv(key)
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

// StdioServerParameters are StdioServerParameters.
type StdioServerParameters struct {
	Command string
	Args    []string
	// Env is merged over GetDefaultEnvironment() (the default environment is always included).
	Env map[string]string
	// Stderr is "inherit" (default), "pipe", "overlapped" or "ignore".
	Stderr string
	Cwd    string
	// MaxBufferSize bounds buffered stdout (default 10 MiB).
	MaxBufferSize int
}

// StdioClientTransport is the SDK's client-side stdio transport: it spawns the server process
// and speaks newline-delimited JSON-RPC over its stdin/stdout.
//
// Differences from Node: the command is resolved with exec.LookPath against this process's PATH
// (cross-spawn/libuv use the child's env PATH); an absolute command, as the CLI uses, behaves
// identically.
type StdioClientTransport struct {
	handlers handlerBox
	params   StdioServerParameters

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	exited  chan struct{}
	rb      *ReadBuffer
	stderr  io.Reader
	started bool
	writeMu sync.Mutex
}

// NewStdioClientTransport creates the transport.
func NewStdioClientTransport(params StdioServerParameters) *StdioClientTransport {
	return &StdioClientTransport{params: params, rb: NewReadBuffer(params.MaxBufferSize)}
}

// Handlers implements Transport.
func (t *StdioClientTransport) Handlers() Handlers { return t.handlers.get() }

// SetHandlers implements Transport.
func (t *StdioClientTransport) SetHandlers(h Handlers) { t.handlers.set(h) }

// SessionID implements Transport.
func (t *StdioClientTransport) SessionID() string { return "" }

// Stderr returns the child's stderr when Stderr is "pipe" or "overlapped".
func (t *StdioClientTransport) Stderr() io.Reader {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stderr
}

// PID returns the child's pid, or 0.
func (t *StdioClientTransport) PID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.cmd.Process == nil {
		return 0
	}
	return t.cmd.Process.Pid
}

// Start spawns the process.
func (t *StdioClientTransport) Start(context.Context) error {
	t.mu.Lock()
	if t.cmd != nil || t.started {
		t.mu.Unlock()
		return errors.New("StdioClientTransport already started! If using Client class, note that connect() calls start() automatically.")
	}
	t.started = true
	t.mu.Unlock()

	env := GetDefaultEnvironment()
	for k, v := range t.params.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	envList := make([]string, 0, len(keys))
	for _, k := range keys {
		envList = append(envList, k+"="+env[k])
	}
	cmd := exec.Command(t.params.Command, t.params.Args...)
	cmd.Env = envList
	cmd.Dir = t.params.Cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.handlers.err(err)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.handlers.err(err)
		return err
	}
	var stderr io.Reader
	switch t.params.Stderr {
	case "", "inherit":
		cmd.Stderr = os.Stderr
	case "pipe", "overlapped":
		r, err := cmd.StderrPipe()
		if err != nil {
			t.handlers.err(err)
			return err
		}
		stderr = r
	case "ignore":
	}
	if err := cmd.Start(); err != nil {
		t.handlers.err(err)
		return err
	}
	exited := make(chan struct{})
	t.mu.Lock()
	t.cmd, t.stdin, t.exited, t.stderr = cmd, stdin, exited, stderr
	t.mu.Unlock()
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				t.ondata(append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
					t.handlers.err(err)
				}
				break
			}
		}
		_ = cmd.Wait()
		close(exited)
		t.mu.Lock()
		if t.cmd == cmd {
			t.cmd, t.stdin = nil, nil
		}
		t.mu.Unlock()
		t.handlers.close()
	}()
	return nil
}

func (t *StdioClientTransport) ondata(chunk []byte) {
	t.mu.Lock()
	err := t.rb.Append(chunk)
	t.mu.Unlock()
	if err != nil {
		t.handlers.err(err)
		go func() { _ = t.Close() }()
		return
	}
	for {
		t.mu.Lock()
		m, err := t.rb.ReadMessage()
		t.mu.Unlock()
		if err != nil {
			t.handlers.err(err)
			continue
		}
		if m == nil {
			return
		}
		t.handlers.message(m)
	}
}

// Close ends stdin, waits up to 2s for the process to exit, then sends SIGTERM, waits up to 2s
// more, then SIGKILL. onclose fires when the process exits (the 'close' event).
func (t *StdioClientTransport) Close() error {
	t.mu.Lock()
	cmd, stdin, exited := t.cmd, t.stdin, t.exited
	t.cmd, t.stdin = nil, nil
	t.mu.Unlock()
	if cmd != nil {
		if stdin != nil {
			_ = stdin.Close()
		}
		wait := func() bool {
			select {
			case <-exited:
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
		if !wait() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			if !wait() {
				_ = cmd.Process.Kill()
			}
		}
	}
	t.mu.Lock()
	t.rb.Clear()
	t.mu.Unlock()
	return nil
}

// Send writes one message line to the child's stdin.
func (t *StdioClientTransport) Send(_ context.Context, m Message, _ *SendOptions) error {
	t.mu.Lock()
	stdin := t.stdin
	t.mu.Unlock()
	if stdin == nil {
		return &SdkError{Code: ErrNotConnected, Message: "Not connected"}
	}
	line, err := serializeLine(m)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := stdin.Write(line); err != nil {
		t.handlers.err(err)
	}
	return nil
}
