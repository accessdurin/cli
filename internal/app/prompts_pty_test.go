//go:build !windows

package app

import (
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/accessdurin/cli/internal/contracts"
)

// ptyTerminal is the user's side of a pseudo-terminal whose other side runs the prompts.
type ptyTerminal struct {
	t      *testing.T
	master *os.File
	mu     sync.Mutex
	output strings.Builder
}

var escapes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78]`)

func newPtyTerminal(t *testing.T) (*ptyTerminal, *os.File) {
	t.Setenv("TERM", "xterm-256color")
	master, tty, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	t.Cleanup(func() { master.Close(); tty.Close() })
	if err := pty.Setsize(master, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	term := &ptyTerminal{t: t, master: master}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			term.mu.Lock()
			term.output.Write(buffer[:n])
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return term, tty
}

func (term *ptyTerminal) plain() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return escapes.ReplaceAllString(term.output.String(), "")
}

// waitFor waits for text to appear on screen.
func (term *ptyTerminal) waitFor(text string) {
	term.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if strings.Contains(term.plain(), text) {
			return
		}
	}
	term.t.Fatalf("terminal never showed %q:\n%s", text, term.plain())
}

// expect waits for text to appear on screen, then types keys.
func (term *ptyTerminal) expect(text, keys string) {
	term.t.Helper()
	term.waitFor(text)
	time.Sleep(50 * time.Millisecond)
	_, _ = io.WriteString(term.master, keys)
}

// TestPromptsInAPseudoTerminal answers prompts as a user does, matching them by their text:
// arrow keys and Enter select, typing and Enter answers, Ctrl-C cancels.
func TestPromptsInAPseudoTerminal(t *testing.T) {
	term, tty := newPtyTerminal(t)
	ui := TerminalUI(tty, tty)
	ctx := context.Background()
	organizations := []contracts.Organization{{ID: "org_one", Name: hostileName}, {ID: "org_two", Name: "Two"}}

	done := make(chan struct{})
	var chosen, name string
	var chooseErr, nameErr, cancelErr error
	go func() {
		defer close(done)
		chosen, chooseErr = ui.Organizations.Choose(ctx, organizations)
		name, nameErr = ui.Organizations.Create(ctx)
		_, cancelErr = ui.Agent.Choose(ctx)
	}()
	term.expect("Which organization should this agent use?", "\x1b[B\r")
	term.expect("What is your organization called?", "A\r")
	term.expect("Enter an organization name of 2–80 characters.", "cme Corp \r")
	term.expect("Which agent would you like to use?", "\x03")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("prompts did not finish:\n%s", term.plain())
	}
	if chooseErr != nil || chosen != "org_two" {
		t.Errorf("choose: %q %v", chosen, chooseErr)
	}
	if nameErr != nil || name != "Acme Corp " {
		t.Errorf("create: %q %v", name, nameErr)
	}
	if cancelErr != errSetupCancelled {
		t.Errorf("cancel: %v", cancelErr)
	}
	term.waitFor("Cancelled.")
	screen := term.plain()
	for _, want := range []string{"Acme Corp", "Claude Code (", "Custom harness (Use a harness descriptor JSON file)"} {
		if !strings.Contains(screen, want) {
			t.Errorf("missing %q:\n%s", want, screen)
		}
	}
	term.mu.Lock()
	raw := term.output.String()
	term.mu.Unlock()
	if strings.Contains(raw, "]52;") || strings.Contains(raw, "\u202e") || strings.Contains(raw, "\u009b") {
		t.Errorf("a server-supplied name reached the terminal unsanitized")
	}
}
