//go:build !windows

package app

import (
	"io"
	"os"

	"golang.org/x/term"
)

// promptReader is where questions read from: a session of the shared terminal input when in
// is a terminal, else in itself. done ends the session.
func (p *prompter) promptReader() (io.Reader, func()) {
	if p.terminal == nil {
		if f, ok := p.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			p.terminal = newPromptInput(f)
		}
	}
	if p.terminal == nil {
		return p.in, func() {}
	}
	session := p.terminal.session()
	return session, func() { _ = session.Close() }
}
