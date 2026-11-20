// Package clierror holds the CLI's expected-failure error type. Its message is shown to the
// user as written (after terminal sanitizing) and it is never reported to telemetry; any
// other error is unexpected and shown only as a generic message.
package clierror

import "errors"

// Error is an expected failure with a user-facing message (CliError in the TypeScript CLI).
type Error struct {
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// New returns an expected failure with message.
func New(message string) error { return &Error{Message: message} }

// Wrap returns an expected failure with message that keeps cause for errors.Is/As.
func Wrap(message string, cause error) error { return &Error{Message: message, Cause: cause} }

// As reports whether err is (or wraps) an expected failure, and returns it.
func As(err error) (*Error, bool) {
	var target *Error
	ok := errors.As(err, &target)
	return target, ok
}
