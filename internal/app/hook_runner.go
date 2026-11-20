package app

import (
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// HookFormats are the hook configuration dialects.
var HookFormats = []string{"claude", "codex", "cursor", "windsurf", "opencode", "gemini", "copilot", "pi", "kiro", "custom"}

var harnessPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// HookInstallationSchema is hookInstallationSchema: <config>/hooks/<harness>.json. The
// checker is absent when hooks only capture; handlers maps each registered event to the
// handler written for it.
var HookInstallationSchema = zod.Object(
	zod.F("format", zod.Enum(HookFormats...)),
	zod.F("harness", zod.String().Regex(harnessPattern)),
	zod.F("checker", zod.Optional(CheckerSchema)),
	zod.F("target", zod.String()),
	zod.F("installedAt", zod.String()),
	zod.F("handler", zod.Optional(recordSchema)),
	zod.F("handlers", zod.Optional(zod.Record(recordSchema))),
	zod.F("assetDigest", zod.String().Regex(regexp.MustCompile(`^[a-f0-9]{64}$`))),
).Strict()

// HookInstallation is a parsed installation record.
type HookInstallation struct {
	Format      string
	Harness     string
	Checker     *contracts.HookChecker // nil when hooks only capture
	Target      string
	InstalledAt string
	Handler     *jsjson.Object // nil when absent
	Handlers    map[string]*jsjson.Object
	AssetDigest string
}

// ParseHookInstallation is hookInstallationSchema.parse (a *zod.Error when invalid).
func ParseHookInstallation(value jsjson.Value) (HookInstallation, error) {
	out, err := zod.Parse(HookInstallationSchema, value)
	if err != nil {
		return HookInstallation{}, err
	}
	o := out.(*jsjson.Object)
	h := HookInstallation{}
	h.Format, _ = o.GetString("format")
	h.Harness, _ = o.GetString("harness")
	if checker, ok := o.GetObject("checker"); ok {
		c := checkerFrom(checker)
		h.Checker = &c
	}
	h.Target, _ = o.GetString("target")
	h.InstalledAt, _ = o.GetString("installedAt")
	h.Handler, _ = o.GetObject("handler")
	if handlers, ok := o.GetObject("handlers"); ok {
		h.Handlers = map[string]*jsjson.Object{}
		for _, key := range handlers.Keys() {
			v, _ := handlers.GetObject(key)
			h.Handlers[key] = v
		}
	}
	h.AssetDigest, _ = o.GetString("assetDigest")
	return h, nil
}

// HookInstallationPath is <directory>/hooks/<harness>.json for a valid harness id.
func HookInstallationPath(directory, harness string) (string, error) {
	if !harnessPattern.MatchString(harness) {
		return "", clierror.New("Invalid harness identifier.")
	}
	return filepath.Join(directory, "hooks", harness+".json"), nil
}

// ReadHookInstallation reads the installation record for harness.
func ReadHookInstallation(directory, harness string) (HookInstallation, error) {
	path, err := HookInstallationPath(directory, harness)
	if err != nil {
		return HookInstallation{}, err
	}
	raw, found, err := regularText(path)
	if err != nil {
		return HookInstallation{}, err
	}
	if !found {
		return HookInstallation{}, clierror.New("No local checker configured. Run durin in an interactive terminal.")
	}
	value, err := jsjson.ParseString(raw)
	if err != nil {
		return HookInstallation{}, err
	}
	return ParseHookInstallation(value)
}

const maxHookInput = 1_048_576

func readHookInput(input io.Reader) (jsjson.Value, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxHookInput+1))
	if err != nil {
		return nil, err
	}
	return parseHookInput(data)
}

func parseHookInput(data []byte) (jsjson.Value, error) {
	if len(data) > maxHookInput {
		return nil, clierror.New("Local hook input exceeds 1 MiB.")
	}
	text := jsjson.DecodeUTF8(data)
	if len(text) >= 3 && text[:3] == "\uFEFF" {
		text = text[3:]
	}
	return jsjson.ParseString(text)
}

// RunHook normalizes the harness's event from input and runs the selected checker on it.
func RunHook(installation HookInstallation, input io.Reader) error {
	value, err := readHookInput(input)
	if err != nil {
		return err
	}
	event, err := NormalizeHook(value, installation.Format)
	if err != nil {
		return err
	}
	if installation.Checker == nil {
		return clierror.New("No local checker configured. Run durin in an interactive terminal.")
	}
	checker, err := ValidateChecker(CheckerJSON(*installation.Checker))
	if err != nil {
		return err
	}
	return invokeChecker(checker, jsjson.Stringify(event.Clone().Set("durin_harness", jsjson.String(installation.Harness))))
}

func invokeChecker(checker contracts.HookChecker, input string) error {
	cmd := exec.Command(checker.Command, checker.Args...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return clierror.New("The local checker could not start.")
	}
	if err := cmd.Start(); err != nil {
		return clierror.New("The local checker could not start.")
	}
	go func() {
		_, _ = io.WriteString(stdin, input)
		_ = stdin.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(checker.TimeoutMs)*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return clierror.New("The local checker timed out.")
	case <-done:
	}
	switch cmd.ProcessState.ExitCode() {
	case 0:
		return nil
	case 2:
		return clierror.New("The local checker denied this tool call.")
	}
	return clierror.New("The local checker failed.")
}
