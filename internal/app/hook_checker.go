package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

// absolutePath is an absolute path without line breaks or NUL (src/hook-checker.ts and the
// other modules that validate paths).
func absolutePath() *zod.StringSchema {
	return zod.String().Min(1).Refine(isAbsoluteHere, "Invalid input").
		Refine(func(s string) bool { return !strings.ContainsAny(s, "\r\n\x00") }, "Invalid input")
}

// CheckerSchema is checkerSchema.
var CheckerSchema = zod.Object(
	zod.F("command", absolutePath()),
	zod.F("args", zod.Default(zod.Array(zod.String().Refine(func(s string) bool { return !strings.Contains(s, "\x00") }, "Invalid input")).Max(64),
		func() jsjson.Value { return jsjson.Array{} })),
	zod.F("timeoutMs", zod.Default(zod.Number().Int().Min(50).Max(30_000), func() jsjson.Value { return jsjson.Number(5000) })),
).Strict()

// CheckerJSON is a checker as an object in schema order.
func CheckerJSON(c contracts.HookChecker) *jsjson.Object {
	args := c.Args
	if args == nil {
		args = []string{}
	}
	return jsjson.Obj(
		jsjson.KV("command", jsjson.String(c.Command)),
		jsjson.KV("args", jsjson.Strings(args...)),
		jsjson.KV("timeoutMs", jsjson.Int(int64(c.TimeoutMs))),
	)
}

func checkerFrom(o *jsjson.Object) contracts.HookChecker {
	c := contracts.HookChecker{Args: []string{}}
	c.Command, _ = o.GetString("command")
	args, _ := o.GetArray("args")
	for _, a := range args {
		c.Args = append(c.Args, string(a.(jsjson.String)))
	}
	timeout, _ := o.GetNumber("timeoutMs")
	c.TimeoutMs = int(timeout)
	return c
}

// ValidateChecker checks a checker's shape and that its command is an executable file.
func ValidateChecker(value jsjson.Value) (contracts.HookChecker, error) {
	out, zerr := zod.SafeParse(CheckerSchema, value)
	if zerr != nil {
		return contracts.HookChecker{}, clierror.New("Invalid checker. Use an absolute executable, args array, and timeoutMs (50–30000).")
	}
	checker := checkerFrom(out.(*jsjson.Object))
	info, err := os.Stat(checker.Command)
	if err != nil || !executable(checker.Command) || !info.Mode().IsRegular() {
		return contracts.HookChecker{}, clierror.New("The selected local checker is missing or is not executable.")
	}
	return checker, nil
}

// ReadChecker reads and validates a checker descriptor file.
func ReadChecker(path string) (contracts.HookChecker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return contracts.HookChecker{}, err
	}
	value, err := jsjson.Parse(data)
	if err != nil {
		return contracts.HookChecker{}, err
	}
	return ValidateChecker(value)
}

// EndpointChecker finds an installed Durin Endpoint helper for claude or codex (macOS only);
// nil when there is none.
func EndpointChecker(agent string) *contracts.HookChecker {
	if agent != "claude" && agent != "codex" {
		return nil
	}
	for _, candidate := range endpointCommands(agent) {
		if checker, err := ValidateChecker(CheckerJSON(candidate)); err == nil {
			return &checker
		}
	}
	return nil
}

func endpointCommands(agent string) []contracts.HookChecker {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var out []contracts.HookChecker
	for _, root := range []string{filepath.Join(Homedir(), "Applications"), "/Applications"} {
		out = append(out, contracts.HookChecker{
			Command:   filepath.Join(root, "Durin Endpoint Client.app", "Contents", "Helpers", "observer-preflight"),
			Args:      []string{"--client", agent},
			TimeoutMs: 10_000,
		})
	}
	return out
}
