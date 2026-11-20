package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/credentials"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/observer"
	"github.com/accessdurin/cli/internal/observer/service"
	"github.com/accessdurin/cli/internal/version"
)

// captureSendBudget is the longest a hook waits to hand its event to the observer. A missing
// or stuck observer costs a hook at most this.
const captureSendBudget = 150 * time.Millisecond

// forwardHookEvent sends a hook payload to the observer in the background; the returned
// function waits for it, at most captureSendBudget after the start.
func forwardHookEvent(directory, harness, event string, payload []byte) (wait func()) {
	if len(payload) > maxHookInput {
		payload = payload[:maxHookInput]
	}
	ctx, cancel := context.WithTimeout(context.Background(), captureSendBudget)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = observer.Send(ctx, observer.Address(directory), observer.Message{
			Harness: harness, Event: event, Time: time.Now(), PID: os.Getpid(), PPID: os.Getppid(),
			Payload: observer.EventPayload(bytes.TrimPrefix(payload, []byte("\xef\xbb\xbf"))),
		})
	}()
	return func() {
		<-done
		cancel()
	}
}

// ForwardHookInput reads a pre-tool hook's input for a handler installed before hook
// events had names (no --event), forwards it as a "PreToolUse" event and returns the input
// for the checker unchanged.
func ForwardHookInput(directory, harness string, stdin io.Reader) (io.Reader, func()) {
	data, _ := io.ReadAll(io.LimitReader(stdin, maxHookInput+1))
	if !harnessPattern.MatchString(harness) {
		return bytes.NewReader(data), func() {}
	}
	return bytes.NewReader(data), forwardHookEvent(directory, harness, "PreToolUse", data)
}

// IsCaptureHook reports whether args run a capture-only hook event: one that must never
// fail or block the harness, whatever goes wrong. It decides from event names alone and never
// reads a record (which may sit in a directory durin refused): a run is capture-only only
// when it names an event and every --event value, however repeated, is an event no format
// checks.
func IsCaptureHook(args []string) bool {
	if len(args) < 2 || args[0] != "hooks" || args[1] != "run" {
		return false
	}
	decoded := make([]string, len(args))
	for i, a := range args {
		decoded[i] = jsjson.DecodeUTF8([]byte(a))
	}
	events := 0
	for _, token := range argsToTokens(decoded) {
		if token.positional || token.name != "event" {
			continue
		}
		if token.value == nil || isCheckEventName(*token.value) {
			return false
		}
		events++
	}
	return events > 0
}

func isCheckEventName(event string) bool {
	for _, events := range HarnessEvents {
		for _, e := range events {
			if e.Check && e.Name == event {
				return true
			}
		}
	}
	return false
}

// SingleHookFlags rejects a hooks run that repeats --harness or --event: the parser keeps
// the last value, and durin's handlers never repeat them.
func SingleHookFlags(options *CliOptions) error {
	if options.Values.Count("harness") > 1 || options.Values.Count("event") > 1 {
		return clierror.New("Give --harness and --event at most once.")
	}
	return nil
}

// RunHookEvent is `durin hooks run --event <name>`: the payload is forwarded to the
// observer, the pre-tool event runs the configured checker exactly as `hooks run` always has
// (same exit codes and fail-closed errors), and every other event continues.
func RunHookEvent(options *CliOptions, streams *IO, harness, event string) error {
	data, _ := io.ReadAll(io.LimitReader(streams.Stdin, maxHookInput+1))
	format := harness
	record, recordErr := ReadHookInstallation(options.Directory, harness)
	if recordErr == nil {
		format = record.Format
	}
	valid := harnessPattern.MatchString(harness) && hookEventPattern.MatchString(event)
	e, known := lookupHookEvent(format, event)
	if valid {
		defer forwardHookEvent(options.Directory, harness, event, data)()
	}
	if !known && isCheckEventName(event) {
		// A check event without a usable record, or under a format that has no such check,
		// fails closed instead of passing as capture.
		if recordErr != nil {
			return recordErr
		}
		return clierror.New("This hook installation has no check for this event. Run durin in an interactive terminal.")
	}
	output := ""
	if known && e.Check {
		if recordErr != nil {
			return recordErr
		}
		if record.Checker != nil {
			if err := RunHook(record, bytes.NewReader(data)); err != nil {
				return err
			}
			output = e.Output
		} else if !e.Respond {
			output = e.Output
		}
	} else if known {
		output = e.Output
	}
	if output != "" {
		_, err := streams.Stdout.Write([]byte(output + "\n"))
		return err
	}
	return nil
}

func observerSpec(directory, executable string) service.Spec {
	return service.Spec{Executable: executable, ConfigDir: directory, Home: Homedir(), GOOS: runtime.GOOS, UID: os.Getuid(), Run: service.ExecRunner}
}

// InstallObserverService installs or updates the observer service for the runtime at
// executable, and describes the outcome for setup. Failures do not fail setup: hooks keep
// working without the observer.
func InstallObserverService(ctx context.Context, directory, executable string) string {
	spec := observerSpec(directory, executable)
	if err := spec.Install(ctx); err != nil {
		return "not started (" + err.Error() + "); hooks keep working without it. Retry with durin observer install"
	}
	return "installed as a " + spec.Manager() + " service"
}

// ObserveCommand is `durin observe`: the observer itself, as the service runs it.
func ObserveCommand(ctx context.Context, options *CliOptions, streams *IO) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if options.Values.Flag("supervise") {
		return supervise(ctx, options.Directory, streams.Stderr)
	}
	config, err := observer.LoadConfig(options.Directory)
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
	}
	logf := func(format string, args ...any) { fmt.Fprintf(streams.Stderr, format+"\n", args...) }
	resolver, err := pinnedResolver(os.Getenv("DURIN_OBSERVER_RESOLVE"))
	if err != nil {
		return clierror.New(err.Error())
	}
	o := observer.New(observer.Options{
		Resolver:  resolver,
		Version:   version.Version,
		ConfigDir: options.Directory,
		Config:    config,
		Keys:      credentials.NewNative(credentialService),
		DNS:       observer.DNSSources(runtime.GOOS, observer.Providers),
		Logf:      logf,
	})
	return o.Run(ctx)
}

// pinnedResolver answers provider hostnames from DURIN_OBSERVER_RESOLVE
// ("host=address,host=address", provider hostnames only) and everything else from the
// system resolver. It exists for tests and for diagnosing detection on networks with
// split-horizon DNS; it can only narrow what is matched, never add non-provider hosts.
func pinnedResolver(spec string) (observer.Resolver, error) {
	if spec == "" {
		return nil, nil
	}
	pinned := pinned{}
	for _, entry := range strings.Split(spec, ",") {
		host, address, ok := strings.Cut(strings.TrimSpace(entry), "=")
		addr, err := netip.ParseAddr(address)
		if !ok || err != nil || !observer.IsProviderHost(host) {
			return nil, fmt.Errorf("DURIN_OBSERVER_RESOLVE: %q is not provider-host=address", entry)
		}
		pinned[strings.ToLower(host)] = append(pinned[strings.ToLower(host)], addr)
	}
	return pinned, nil
}

type pinned map[string][]netip.Addr

func (p pinned) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if addrs, ok := p[host]; ok {
		return addrs, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

// supervise runs the observer as a child process and restarts it when it exits, for
// service managers that do not restart crashed processes themselves (Task Scheduler).
func supervise(ctx context.Context, directory string, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	backoff := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.CommandContext(ctx, executable, "observe", "--config-dir", directory)
		cmd.Stderr = stderr
		hideWindow(cmd)
		err := cmd.Run()
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintf(stderr, "observer exited (%v); restarting\n", err)
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	return nil
}

type observerStatus struct {
	Service    service.Status          `json:"service"`
	Address    string                  `json:"address"`
	Running    bool                    `json:"running"`
	Observer   *observer.Stats         `json:"observer"`
	Capture    *observer.CaptureStats  `json:"capture,omitempty"`
	Config     observer.Config         `json:"config"`
	DNSSources []observerSourceSummary `json:"availableDnsSources"`
}

type observerSourceSummary struct {
	Source      string `json:"source"`
	Description string `json:"description"`
}

// ObserverCommand is `durin observer status|install|remove|purge|export`.
func ObserverCommand(ctx context.Context, options *CliOptions, streams *IO) error {
	directory := options.Directory
	runtimeExecutable := RuntimeExecutablePath(directory)
	spec := observerSpec(directory, runtimeExecutable)
	address := observer.Address(directory)
	switch positional(options, 1) {
	case "status":
		config, err := observer.LoadConfig(directory)
		if err != nil {
			return clierror.New(err.Error())
		}
		status := observerStatus{Service: spec.Status(ctx), Address: address, Config: config, DNSSources: []observerSourceSummary{}}
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		stats, err := observer.Query(queryCtx, address)
		cancel()
		if err == nil {
			status.Running, status.Observer = true, &stats
		} else {
			summary := observer.CaptureSummary(directory, config)
			status.Capture = &summary
		}
		for _, s := range observer.DNSSources(runtime.GOOS, observer.Providers) {
			status.DNSSources = append(status.DNSSources, observerSourceSummary{s.Name(), s.Description()})
		}
		return writeJSON(streams.Stdout, status)
	case "install":
		executable, err := CurrentExecutable()
		if err != nil {
			return err
		}
		installed, err := InstallRuntime(directory, executable)
		if err != nil {
			return err
		}
		spec = observerSpec(directory, installed)
		if err := spec.Install(ctx); err != nil {
			return clierror.New("The observer service could not be installed: " + err.Error())
		}
		return writeJSON(streams.Stdout, spec.Status(ctx))
	case "remove":
		if err := spec.Remove(ctx); err != nil {
			return clierror.New("The observer service could not be removed: " + err.Error())
		}
		return writeJSON(streams.Stdout, spec.Status(ctx))
	case "purge":
		purgeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := observer.RequestPurge(purgeCtx, address)
		cancel()
		if err != nil {
			n, err := observer.PurgeAll(observer.CaptureDirectory(directory))
			if err != nil {
				return err
			}
			result = observer.PurgeResult{Deleted: n}
		}
		if result.Error != "" {
			return clierror.New("The capture could not be purged: " + result.Error)
		}
		return writeJSON(streams.Stdout, map[string]int{"deletedSegments": result.Deleted})
	case "export":
		key, err := observer.LoadKey(credentials.NewNative(credentialService), false)
		if err != nil {
			return clierror.New("No readable capture: " + err.Error())
		}
		out := bytesWriter{streams.Stdout}
		skipped, err := observer.ReadCapture(observer.CaptureDirectory(directory), key, func(record []byte) error {
			return out.line(record)
		})
		if err != nil {
			return err
		}
		if skipped > 0 {
			fmt.Fprintf(streams.Stderr, "%d capture segments were sealed with another key and were skipped.\n", skipped)
		}
		return nil
	}
	return clierror.New("Use durin observer status, install, remove, purge or export.")
}

type bytesWriter struct{ w io.Writer }

func (b bytesWriter) line(record []byte) error {
	_, err := b.w.Write(append(append([]byte(nil), record...), '\n'))
	return err
}

func writeJSON(w io.Writer, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}
