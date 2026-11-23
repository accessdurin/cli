package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// ReportingSchema is <config>/reporting/<harness>.json: setup's record that it configured
// the harness to report calls to local MCP servers (src/report-hook.ts).
var ReportingSchema = zod.Object(
	zod.F("harness", zod.String().Regex(harnessPattern)),
	zod.F("format", zod.String().Refine(isReportFormat, "Unsupported reporting format.")),
	zod.F("profileId", zod.String().Min(1).Max(128)),
	zod.F("target", zod.String()),
	zod.F("command", zod.String()),
	zod.F("mcpConfig", zod.Object(
		zod.F("configPath", zod.String().Min(1)),
		zod.F("format", zod.Enum("jsonc", "toml")),
		zod.F("serverKey", zod.String().Min(1).Max(64)),
	).Strict()),
	zod.F("installedAt", zod.String()),
).Strict()

// ReportingInstallation is a parsed reporting record.
type ReportingInstallation struct {
	Harness, Format, ProfileID, Target, Command, InstalledAt string
	McpConfig                                                McpConfigLocation
}

// ReportingPath is <directory>/reporting/<harness>.json for a valid harness id.
func ReportingPath(directory, harness string) (string, error) {
	if !harnessPattern.MatchString(harness) {
		return "", fmt.Errorf("invalid harness identifier")
	}
	return filepath.Join(directory, "reporting", harness+".json"), nil
}

// ReadReporting reads the reporting record for harness; nil when reporting is not installed.
func ReadReporting(directory, harness string) (*ReportingInstallation, error) {
	path, err := ReportingPath(directory, harness)
	if err != nil {
		return nil, err
	}
	raw, found, err := regularText(path)
	if err != nil || !found {
		return nil, nil
	}
	value, err := jsjson.ParseString(raw)
	if err != nil {
		return nil, err
	}
	out, err := zod.Parse(ReportingSchema, value)
	if err != nil {
		return nil, err
	}
	o := out.(*jsjson.Object)
	config, _ := o.GetObject("mcpConfig")
	return &ReportingInstallation{
		Harness:     eventString(o, "harness"),
		Format:      eventString(o, "format"),
		ProfileID:   eventString(o, "profileId"),
		Target:      eventString(o, "target"),
		Command:     eventString(o, "command"),
		InstalledAt: eventString(o, "installedAt"),
		McpConfig: McpConfigLocation{
			ConfigPath: eventString(config, "configPath"),
			Format:     eventString(config, "format"),
			ServerKey:  eventString(config, "serverKey"),
		},
	}, nil
}

// encodedArguments is the call's arguments as a JSON object of at most 16 KiB; larger
// arguments become "{}", marked truncated. A non-object is wrapped as {value: ...}.
func encodedArguments(input jsjson.Value) (string, bool) {
	var object jsjson.Value
	switch v := input.(type) {
	case *jsjson.Object:
		object = v
	case nil:
		object = jsjson.NewObject()
	default:
		object = jsjson.Obj(jsjson.KV("value", v))
	}
	text := jsjson.Stringify(object)
	if len(text) <= contracts.LocalToolCallArgumentBytes {
		return text, false
	}
	return "{}", true
}

// reportIdentifier is a valid identifier, or null.
func reportIdentifier(value string) jsjson.Value {
	if value == "" {
		return jsjson.Null{}
	}
	out, err := zod.SafeParse(contracts.ReportIdentifierSchema, jsjson.String(value))
	if err != nil {
		return jsjson.Null{}
	}
	return out
}

// reportID is a stable ID per agent call, so a hook that fires twice stores the call once.
func reportID(harness string, call ObservedCall) string {
	if call.SessionID == "" || call.ToolCallID == "" {
		return randomUUID()
	}
	sum := sha256.Sum256([]byte(harness + "\x00" + call.SessionID + "\x00" + call.ToolCallID))
	h := hex.EncodeToString(sum[:])
	nibble, _ := strconv.ParseUint(h[16:17], 16, 8)
	variant := strconv.FormatUint(nibble&0x3|0x8, 16)
	return h[0:8] + "-" + h[8:12] + "-5" + h[13:16] + "-" + variant + h[17:20] + "-" + h[20:32]
}

// LocalToolCall is the report for a call to a local server; nil when Durin would refuse it
// (for example, a server name it does not accept). Tool results are never included.
func LocalToolCall(harness string, call ObservedCall, server LocalServer, now time.Time) *jsjson.Object {
	argumentsJSON, truncated := encodedArguments(call.Input)
	out, err := zod.SafeParse(contracts.LocalToolCallSchema, jsjson.Obj(
		jsjson.KV("reportId", jsjson.String(reportID(harness, call))),
		jsjson.KV("reporter", jsjson.String("cli")),
		jsjson.KV("agent", jsjson.String(harness)),
		jsjson.KV("sessionId", reportIdentifier(call.SessionID)),
		jsjson.KV("turnId", reportIdentifier(call.TurnID)),
		jsjson.KV("toolCallId", reportIdentifier(call.ToolCallID)),
		jsjson.KV("mcpServer", jsjson.String(server.Server)),
		jsjson.KV("tool", jsjson.String(server.Tool)),
		jsjson.KV("transport", jsjson.String(server.Transport)),
		jsjson.KV("outcome", jsjson.String(call.Outcome)),
		jsjson.KV("argumentsJson", jsjson.String(argumentsJSON)),
		jsjson.KV("argumentsTruncated", jsjson.Bool(truncated)),
		jsjson.KV("observedAt", jsjson.String(isoString(now))),
	))
	if err != nil {
		return nil
	}
	return out.(*jsjson.Object)
}

// ReportEventLimit bounds a post-tool event. The payload carries the tool's result, which is
// never sent but can be large.
const ReportEventLimit = 16 * 1_048_576

// readReportEvent is the event on input; nil when it exceeds ReportEventLimit.
func readReportEvent(input io.Reader) (jsjson.Value, error) {
	data, err := io.ReadAll(io.LimitReader(input, ReportEventLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > ReportEventLimit {
		return nil, nil
	}
	text := strings.TrimPrefix(jsjson.DecodeUTF8(data), "\uFEFF")
	return jsjson.ParseString(text)
}

// FlushArguments is the command the report hook starts in the background:
// <executable> hooks flush --profile <id> --config-dir <directory>.
func FlushArguments(executable, directory, profileID string) []string {
	return []string{executable, "hooks", "flush", "--profile", profileID, "--config-dir", directory}
}

// launchFlush starts a detached flush and does not wait for it.
func launchFlush(directory, profileID string) {
	executable, err := os.Executable()
	if err != nil {
		return
	}
	args := FlushArguments(executable, directory, profileID)
	cmd := exec.Command(args[0], args[1:]...)
	detach(cmd)
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// ReportDependencies replaces the report hook's environment in tests. Nil fields use the
// real Endpoint checks, the home directory and a detached flush.
type ReportDependencies struct {
	EndpointPresent func(directory string, installation ReportingInstallation) bool
	Home            string
	Flush           func(directory, profileID string)
	Now             func() time.Time
	LookupEnv       func(name string) (string, bool)
	// Store is the OS credential store the hook checks for the profile's MCP tokens.
	Store ports.CredentialStore
}

const endpointHelper = "observer-preflight"

// endpointCheckerFor finds an installed Endpoint helper; tests replace it.
var endpointCheckerFor = EndpointChecker

// endpointWired reports whether the harness runs Endpoint's helper, either directly or as
// the CLI's configured checker.
func endpointWired(directory string, installation ReportingInstallation) bool {
	if hooks, _, _ := regularText(installation.Target); strings.Contains(hooks, endpointHelper) {
		return true
	}
	checker, err := ReadHookInstallation(directory, installation.Harness)
	return err == nil && checker.Checker != nil && strings.HasSuffix(checker.Checker.Command, endpointHelper)
}

// EndpointReportingMarker is the file the Endpoint app refreshes at least hourly while signed
// in, and removes on sign-out.
func EndpointReportingMarker(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Durin Observer", "Organization", "reporting.json")
}

// EndpointMarkerMaxAge is how old the marker may be before Endpoint counts as signed out.
const EndpointMarkerMaxAge = 2 * time.Hour

func endpointSignedIn(home string, now time.Time) bool {
	info, err := os.Stat(EndpointReportingMarker(home))
	return err == nil && now.Sub(info.ModTime()) <= EndpointMarkerMaxAge
}

// endpointCovers reports whether Endpoint reports this harness's local MCP calls: its helper
// is installed, wired into the harness's hooks, and signed in to an organization. Otherwise
// the CLI reports.
func endpointCovers(home string, now func() time.Time) func(string, ReportingInstallation) bool {
	return func(directory string, installation ReportingInstallation) bool {
		return endpointCheckerFor(installation.Harness) != nil && endpointWired(directory, installation) &&
			endpointSignedIn(home, now())
	}
}

func (d ReportDependencies) resolved() ReportDependencies {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Home == "" {
		d.Home = Homedir()
	}
	if d.EndpointPresent == nil {
		d.EndpointPresent = endpointCovers(d.Home, d.Now)
	}
	if d.Flush == nil {
		d.Flush = launchFlush
	}
	if d.LookupEnv == nil {
		d.LookupEnv = os.LookupEnv
	}
	if d.Store == nil {
		d.Store = NewNativeCredentialStore()
	}
	return d
}

// importerMarkers are payload keys that name the harness that made the call, and
// importerVariables are variables set when another harness runs Claude Code's hooks. They
// match Endpoint's detection (apps/osx/Hook/HookEnvelope.swift).
var (
	importerMarkers   = []struct{ key, harness string }{{"hookEventName", "grok"}, {"cursor_version", "cursor"}}
	importerVariables = []struct{ name, harness string }{{"DEVIN_PROJECT_DIR", "devin"}}
)

// callingHarness is the harness that made the call, which differs from the configured one
// when another harness imports its hooks.
func callingHarness(configured string, event jsjson.Value, lookupEnv func(string) (string, bool)) string {
	fields, _ := event.(*jsjson.Object)
	for _, marker := range importerMarkers {
		if fields != nil && fields.Has(marker.key) {
			return marker.harness
		}
	}
	if configured != "claude" {
		return configured
	}
	for _, variable := range importerVariables {
		if _, found := lookupEnv(variable.name); found {
			return variable.harness
		}
	}
	return configured
}

// Report hook outcomes.
const (
	ReportDisabled  = "disabled"
	ReportEndpoint  = "endpoint"
	ReportIgnored   = "ignored"
	ReportImported  = "imported"
	ReportNotLocal  = "not_local"
	ReportQueued    = "queued"
	ReportSignedOut = "signed_out"
)

// ReportHook handles one post-tool hook event: when Endpoint does not cover the harness, the
// configured harness made the call and it went to a local MCP server, it spools the call and
// starts a background flush. A call from a harness that imports these hooks is not reported,
// as Endpoint does not report it. The caller never lets it block or fail the agent.
func ReportHook(directory, harness string, stdin io.Reader, deps ReportDependencies) (string, error) {
	deps = deps.resolved()
	installation, err := ReadReporting(directory, harness)
	if err != nil {
		return "", err
	}
	if installation == nil {
		return ReportDisabled, nil
	}
	if deps.EndpointPresent(directory, *installation) {
		return ReportEndpoint, nil
	}
	event, err := readReportEvent(stdin)
	if err != nil {
		return "", err
	}
	if event == nil {
		return ReportIgnored, nil
	}
	if callingHarness(installation.Harness, event, deps.LookupEnv) != installation.Harness {
		return ReportImported, nil
	}
	call, err := ObservedMcpCall(installation.Format, event)
	if err != nil {
		return "", err
	}
	if call == nil {
		return ReportIgnored, nil
	}
	server := localServerFor(installation.Format, installation.McpConfig, call.Targets, call.Cwd)
	if server == nil {
		return ReportNotLocal, nil
	}
	report := LocalToolCall(installation.Harness, *call, *server, deps.Now())
	if report == nil {
		return ReportIgnored, nil
	}
	signedIn := func() bool {
		return profileMayReport(directory, installation.ProfileID) && profileSignedIn(deps.Store, installation.ProfileID)
	}
	if !signedIn() {
		return ReportSignedOut, nil
	}
	path, err := spoolCall(SpoolDirectory(directory, installation.ProfileID), report, deps.Now)
	if err != nil {
		return "", err
	}
	// A logout that ran between the check and the write removed the spool before this call
	// reached it (MAR-1881). From its first step until the tokens are gone, logout keeps the
	// profile marked, and it removes the spool after marking, so either it removes this file
	// or this check sees it signed out. TestSignOutModel explores every order.
	if !signedIn() {
		_ = os.Remove(path)
		return ReportSignedOut, nil
	}
	deps.Flush(directory, installation.ProfileID)
	return ReportQueued, nil
}

// profileMayReport reports whether the profile is still saved and has no unfinished sign-out.
func profileMayReport(directory, profileID string) bool {
	path, err := NewFileProfiles(directory).path(profileID)
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	marker, err := NewFileRevocationMarker(directory, profileID)
	if err != nil {
		return false
	}
	pending, _ := marker.Pending()
	return !pending
}

// reportCredentialDeadline bounds the hook's credential-store read, so a locked or hung store
// costs the agent at most this long; tests shorten it.
var reportCredentialDeadline = 2 * time.Second

// profileSignedIn reports whether the store confirms, within reportCredentialDeadline, that it
// holds the profile's MCP tokens. A missing entry, a failed read or no answer in time means
// signed out, so the call's arguments are never written to disk.
func profileSignedIn(store ports.CredentialStore, profileID string) bool {
	found := make(chan bool, 1)
	go func() {
		_, ok, err := store.Read("mcp:" + profileID + ":tokens")
		found <- err == nil && ok
	}()
	select {
	case ok := <-found:
		return ok
	case <-time.After(reportCredentialDeadline):
		return false
	}
}

var reportFileName = regexp.MustCompile(`^([0-9a-f-]{36})(?:\.(\d))?\.json$`)
