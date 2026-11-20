// Package telemetry reports unexpected CLI failures to Sentry (src/telemetry.ts and
// src/telemetry-privacy.ts). Only errors that are neither expected CLI errors nor
// command-line usage errors are reported; events keep no message, user, request, breadcrumbs,
// local paths or host name. Telemetry is off with DO_NOT_TRACK=1/true or NODE_ENV=test, and
// it never fails or changes a command. (sentry-go has no profiler, so the TypeScript CLI's
// profiling is not carried over.)
package telemetry

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/version"
)

const (
	dsn          = "https://19cf98c3143c037e2d0306ffcc283bb5@o4512105381494784.ingest.us.sentry.io/4512354708422656"
	flushTimeout = 2 * time.Second
	scrubbed     = "Durin CLI operation failed"
)

var (
	mu      sync.Mutex
	enabled bool
)

// argumentError is implemented by command-line usage errors (Node's ERR_PARSE_ARGS_*).
type argumentError interface{ IsArgumentError() bool }

func expected(err error) bool {
	if _, ok := clierror.As(err); ok {
		return true
	}
	a, ok := err.(argumentError)
	return ok && a.IsArgumentError()
}

func trackingDisabled() bool {
	v := strings.ToLower(os.Getenv("DO_NOT_TRACK"))
	return v == "1" || v == "true"
}

// Enabled reports whether this process may send telemetry at all.
func Enabled() bool { return os.Getenv("NODE_ENV") != "test" && !trackingDisabled() }

func release() string { return "@accessdurin/cli@" + version.Version }

// guard runs f, swallowing any panic: reporting must never prevent a command from running.
func guard(f func()) {
	defer func() { _ = recover() }()
	f()
}

// Initialize configures Sentry when telemetry is enabled.
func Initialize() {
	if !Enabled() {
		return
	}
	guard(func() {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:                  dsn,
			Release:              release(),
			Environment:          "production",
			EnableTracing:        true,
			TracesSampleRate:     1,
			SendDefaultPII:       false,
			MaxBreadcrumbs:       -1,
			DisableClientReports: true,
			Integrations:         func([]sentry.Integration) []sentry.Integration { return nil },
			HTTPClient:           &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}},
			BeforeSend:           func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event { return ScrubEvent(event) },
			BeforeSendTransaction: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
				return ScrubTransaction(event)
			},
			BeforeBreadcrumb: func(*sentry.Breadcrumb, *sentry.BreadcrumbHint) *sentry.Breadcrumb { return nil },
			BeforeSendLog:    func(*sentry.Log) *sentry.Log { return nil },
			BeforeSendMetric: func(*sentry.Metric) *sentry.Metric { return nil },
		})
		mu.Lock()
		enabled = err == nil
		mu.Unlock()
	})
}

func active() bool {
	mu.Lock()
	defer mu.Unlock()
	return enabled
}

// ReportUnexpected sends err unless it is an expected CLI or usage error.
func ReportUnexpected(err error) {
	if err == nil || expected(err) || !active() {
		return
	}
	guard(func() { sentry.CaptureException(err) })
}

// Flush waits up to two seconds for reports to be delivered.
func Flush() {
	if !active() {
		return
	}
	guard(func() { sentry.Flush(flushTimeout) })
}

// TraceCommand runs action inside a "durin command" transaction; if tracing cannot start, the
// action still runs, once.
func TraceCommand(action func(ctx context.Context) error) error {
	ctx := context.Background()
	var span *sentry.Span
	if active() {
		guard(func() { span = sentry.StartTransaction(ctx, "durin command", sentry.WithOpName("cli.command")) })
	}
	if span == nil {
		return action(ctx)
	}
	defer guard(span.Finish)
	return action(span.Context())
}

var errorTypes = map[string]bool{
	"Error": true, "TypeError": true, "RangeError": true, "ReferenceError": true, "SyntaxError": true,
	"URIError": true, "EvalError": true, "AggregateError": true,
}

func errorType(t string) string {
	if errorTypes[t] {
		return t
	}
	return "Error"
}

// filenameOnly keeps the last path segment, without a query or fragment.
func filenameOnly(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	return p[strings.LastIndex(p, "/")+1:]
}

func safeFrame(frame sentry.Frame) sentry.Frame {
	safe := sentry.Frame{Function: frame.Function, Lineno: frame.Lineno, Colno: frame.Colno, InApp: frame.InApp}
	if frame.Filename != "" {
		safe.Filename = filenameOnly(frame.Filename)
	} else if frame.AbsPath != "" {
		safe.Filename = filenameOnly(frame.AbsPath)
	}
	return safe
}

func safeException(exception sentry.Exception) sentry.Exception {
	handled := true
	mechanism := &sentry.Mechanism{Type: "generic", Handled: &handled}
	if exception.Mechanism != nil {
		mechanism = &sentry.Mechanism{Type: exception.Mechanism.Type, Handled: exception.Mechanism.Handled}
	}
	frames := []sentry.Frame{}
	if exception.Stacktrace != nil {
		for _, f := range exception.Stacktrace.Frames {
			frames = append(frames, safeFrame(f))
		}
	}
	return sentry.Exception{
		Type:       errorType(exception.Type),
		Value:      scrubbed,
		Mechanism:  mechanism,
		Stacktrace: &sentry.Stacktrace{Frames: frames},
	}
}

func safeTrace(contexts map[string]sentry.Context) map[string]sentry.Context {
	out := map[string]sentry.Context{}
	if trace, ok := contexts["trace"]; ok {
		kept := sentry.Context{}
		for _, key := range []string{"trace_id", "span_id", "parent_span_id"} {
			if v, ok := trace[key]; ok {
				kept[key] = v
			}
		}
		out["trace"] = kept
	}
	return out
}

// Tags are the only tags an event carries, with Node's platform and architecture names.
func Tags() map[string]string {
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	arch := map[string]string{"amd64": "x64", "386": "ia32"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	return map[string]string{"component": "cli", "platform": platform, "arch": arch}
}

// ScrubEvent keeps diagnostics useful without sending auth data, tool payloads, or local paths.
func ScrubEvent(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}
	exceptions := make([]sentry.Exception, 0, len(event.Exception))
	for _, e := range event.Exception {
		exceptions = append(exceptions, safeException(e))
	}
	return &sentry.Event{
		EventID:     event.EventID,
		Timestamp:   event.Timestamp,
		Level:       event.Level,
		Sdk:         event.Sdk,
		Type:        event.Type,
		Platform:    "go",
		Release:     release(),
		Environment: "production",
		Tags:        Tags(),
		Contexts:    safeTrace(event.Contexts),
		Exception:   exceptions,
	}
}

var spanAttributes = []string{"trace_id", "span_id", "parent_span_id", "op", "origin", "status"}

// ScrubTransaction keeps a command transaction's timing and identity only.
func ScrubTransaction(event *sentry.Event) *sentry.Event {
	if event == nil {
		return nil
	}
	contexts := map[string]sentry.Context{}
	if trace, ok := event.Contexts["trace"]; ok {
		kept := sentry.Context{}
		for _, key := range spanAttributes {
			if v, ok := trace[key]; ok {
				kept[key] = v
			}
		}
		contexts["trace"] = kept
	}
	return &sentry.Event{
		EventID:         event.EventID,
		Timestamp:       event.Timestamp,
		StartTime:       event.StartTime,
		Level:           event.Level,
		Sdk:             event.Sdk,
		Type:            event.Type,
		Transaction:     "durin command",
		TransactionInfo: event.TransactionInfo,
		Platform:        "go",
		Release:         release(),
		Environment:     "production",
		Tags:            Tags(),
		Contexts:        contexts,
	}
}
