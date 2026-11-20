package app

import "io"

// IO is the process's standard streams and exit status, as commands see them. ExitCode is
// process.exitCode: a command sets it to fail without an error message.
type IO struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	IsTTY    func() bool // stdin and stdout are both terminals
	ExitCode int
}
