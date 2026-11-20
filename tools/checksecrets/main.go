// Command checksecrets fails when a tracked (or untracked, non-ignored) repository file
// looks like it contains a live credential. It names the file and rule, never the value.
//
//	go run ./tools/checksecrets
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var rules = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----\s+[A-Za-z0-9+/=\r\n]{64,}\s+-----END`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{36,}\b`)},
	{"live Stripe key", regexp.MustCompile(`\b(?:sk|rk)_live_[A-Za-z0-9]{16,}\b`)},
	{"Stripe webhook secret", regexp.MustCompile(`\bwhsec_[A-Za-z0-9]{16,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{20,}\b`)},
}

func main() {
	out, err := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Secret scan needs a git checkout:", err)
		os.Exit(1)
	}
	var files []string
	for _, file := range strings.Split(string(out), "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	failed := false
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil || bytes.IndexByte(content, 0) >= 0 {
			continue // deleted in the working tree, or binary
		}
		for _, rule := range rules {
			if rule.pattern.Match(content) {
				if !failed {
					fmt.Fprintln(os.Stderr, "Secret scan failed; review the named files without printing credential values:")
				}
				failed = true
				fmt.Fprintf(os.Stderr, "  %s: %s\n", rule.name, file)
			}
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Printf("Secret scan passed for %d repository files.\n", len(files))
}
