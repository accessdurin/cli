package telemetry

import (
	"errors"
	"testing"

	"github.com/getsentry/sentry-go"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/version"
)

type usageError struct{}

func (usageError) Error() string         { return "usage" }
func (usageError) IsArgumentError() bool { return true }

func TestOnlyUnexpectedErrorsAreReported(t *testing.T) {
	if !expected(clierror.New("x")) || !expected(usageError{}) || expected(errors.New("boom")) {
		t.Fatal("expected-error classification")
	}
}

func TestTrackingCanBeDisabled(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE"} {
		t.Setenv("DO_NOT_TRACK", value)
		if Enabled() {
			t.Fatalf("DO_NOT_TRACK=%s", value)
		}
	}
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("NODE_ENV", "test")
	if Enabled() {
		t.Fatal("NODE_ENV=test")
	}
}

func TestScrubEvent(t *testing.T) {
	handled := false
	event := &sentry.Event{
		EventID: "abc", Level: sentry.LevelError, Message: "token=secret", ServerName: "my-laptop",
		User: sentry.User{ID: "u"}, Tags: map[string]string{"x": "y"},
		Breadcrumbs: []*sentry.Breadcrumb{{Message: "secret"}},
		Contexts:    map[string]sentry.Context{"trace": {"trace_id": "t", "span_id": "s", "data": "secret"}, "os": {"name": "linux"}},
		Exception: []sentry.Exception{{
			Type: "*fs.PathError", Value: "open /home/me/secret: denied", Module: "io/fs",
			Mechanism:  &sentry.Mechanism{Type: "chained", Handled: &handled, Description: "secret"},
			Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{{Function: "run", Filename: "/home/me/cli/main.go?x#y", AbsPath: "/home/me/cli/main.go", Lineno: 3, InApp: true, Vars: map[string]any{"k": "v"}, ContextLine: "secret"}}},
		}},
	}
	got := ScrubEvent(event)
	if got.Message != "" || got.ServerName != "" || got.User.ID != "" || got.Breadcrumbs != nil || got.Release != "@accessdurin/cli@"+version.Version || got.Environment != "production" {
		t.Fatalf("leaked fields: %+v", got)
	}
	if len(got.Tags) != 3 || got.Tags["component"] != "cli" || len(got.Contexts) != 1 || len(got.Contexts["trace"]) != 2 {
		t.Fatalf("tags/contexts: %v %v", got.Tags, got.Contexts)
	}
	e := got.Exception[0]
	frame := e.Stacktrace.Frames[0]
	if e.Type != "Error" || e.Value != "Durin CLI operation failed" || e.Module != "" || e.Mechanism.Type != "chained" || *e.Mechanism.Handled || e.Mechanism.Description != "" ||
		frame.Filename != "main.go" || frame.AbsPath != "" || frame.Vars != nil || frame.ContextLine != "" || frame.Function != "run" || frame.Lineno != 3 {
		t.Fatalf("exception: %+v %+v", e, frame)
	}
}
