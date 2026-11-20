package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
