package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestExitCodesAndMessages(t *testing.T) {
	t.Setenv("DO_NOT_TRACK", "1")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	config := filepath.Join(root, "config")
	file := filepath.Join(root, "f\x1b[2J")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args   []string
		code   int
		stderr string
	}{
		{[]string{"--help"}, 0, ""},
		{[]string{"bogus", "--config-dir", config}, 1, "Unknown command. Run durin --help.\n"},
		{[]string{"verify", "--nope"}, 1, "Unknown option. Run durin --help.\n"},
		{[]string{"verify", "--profile"}, 1, genericFailure + "\n"},
		{[]string{"hooks", "run", "--config-dir", config}, 2, "Invalid harness identifier.\n"},
		{[]string{"--config-dir", config, "hooks", "run"}, 1, "Invalid harness identifier.\n"},
		{[]string{"login", "--config-dir", config}, 1, "Run durin in an interactive terminal to complete onboarding.\n"},
		{[]string{"profiles", "list", "--config-dir", file}, 1, "The Durin directory " + filepath.Join(root, "f") +
			" must be a directory owned by you that only you can access. Run chmod 700 on it, or choose another --config-dir.\n"},
	}
	if runtime.GOOS == "windows" {
		cases = cases[:len(cases)-1] // no private-directory check on Windows
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		code := run(c.args, strings.NewReader(""), &stdout, &stderr, func() bool { return false })
		if code != c.code || stderr.String() != c.stderr {
			t.Errorf("%q: exit %d, stderr %q", c.args, code, stderr.String())
		}
	}
	if got := errorMessage(errors.New("boom"), true); got != "Durin could not complete this local hook check." {
		t.Errorf("local hook failure message %q", got)
	}
}

// MAR-1874: a custom harness whose hook record uses a built-in format (here claude) runs
// the pre-tool check under that format, so a denial must block the tool call (exit 2),
// not be swallowed as a capture-only event (exit 0).
func TestCustomHarnessWithBuiltInFormatDenyBlocks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script checker")
	}
	t.Setenv("DO_NOT_TRACK", "1")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	config := filepath.Join(root, "config")
	if err := os.MkdirAll(filepath.Join(config, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(root, "checker.sh")
	if err := os.WriteFile(checker, []byte("#!/bin/sh\ncat >/dev/null\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := `{"format":"claude","harness":"custom-acme","checker":{"command":` + strconv.Quote(checker) + `,"args":[],"timeoutMs":1000},` +
		`"target":` + strconv.Quote(filepath.Join(root, "settings.json")) + `,"installedAt":"2026-01-01T00:00:00.000Z","assetDigest":"` + strings.Repeat("0", 64) + `"}`
	if err := os.WriteFile(filepath.Join(config, "hooks", "custom-acme.json"), []byte(record+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `{"hook_event_name":"PreToolUse","session_id":"s","tool_use_id":"c","tool_name":"mcp__sandbox__read","tool_input":{}}`
	var stdout, stderr bytes.Buffer
	args := []string{"hooks", "run", "--harness", "custom-acme", "--config-dir", config, "--event", "PreToolUse"}
	if code := run(args, strings.NewReader(input), &stdout, &stderr, func() bool { return false }); code != 2 {
		t.Fatalf("denied pre-tool call on a custom harness with a claude record exited %d (stdout %q, stderr %q), want 2", code, stdout.String(), stderr.String())
	}
	// A capture-only event on the same harness still never blocks.
	stdout.Reset()
	stderr.Reset()
	args[len(args)-1] = "Stop"
	if code := run(args, strings.NewReader(`{}`), &stdout, &stderr, func() bool { return false }); code != 0 {
		t.Fatalf("capture event exited %d (stderr %q), want 0", code, stderr.String())
	}
	// Unparseable options: a capture event still never blocks; a pre-tool event fails closed.
	for event, want := range map[string]int{"Stop": 0, "PreToolUse": 2} {
		bad := []string{"hooks", "run", "--harness", "custom-acme", "--config-dir", config, "--app-url", "http://example.com", "--event", event}
		if code := run(bad, strings.NewReader(`{}`), &stdout, &stderr, func() bool { return false }); code != want {
			t.Errorf("%s with invalid options exited %d, want %d", event, code, want)
		}
	}
}
