package app

import "io"

// promptReader is where questions read from. Windows console input is read through console
// APIs that check for cancellation before consuming input, so questions read in directly.
func (p *prompter) promptReader() (io.Reader, func()) { return p.in, func() {} }
