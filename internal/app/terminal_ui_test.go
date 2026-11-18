package app

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/ports"
)

const hostileName = "Acme\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[1A\x1b[2K\u009b2J\u202e Corp"

// controls matches any C0/C1 control or bidi override other than a line break.
var controls = regexp.MustCompile(`[\x00-\x09\x0b-\x1f\x7f-\x9f\x{202e}]`)

func TestTerminalUIShowsServerTextWithoutTerminalControls(t *testing.T) {
	var out bytes.Buffer
	var opened []string
	ui := &terminalUI{p: newPrompter(strings.NewReader(""), &out), open: func(_ context.Context, url string) error {
		opened = append(opened, url)
		return nil
	}}
	ctx := context.Background()
	if err := (accountQuestions{ui}).Authorize(ctx, contracts.AuthorizationPrompt{Code: hostileName, URL: "https://auth.example.test/device"}, ports.IntentSignup); err != nil {
		t.Fatal(err)
	}
	_ = (onboardingQuestions{ui}).Payment(ctx, hostileName)
	agent := agentQuestions{ui}
	agent.Configured("/home/user/.claude.json")
	agent.Integrations(ports.IntegrationsResult{Skills: 5, Hooks: true, Custom: true})
	agent.Ready(ports.SetupReady{Organization: hostileName, Agent: "Codex", ProfileID: strings.Repeat("a", 64), ToolCount: 0})
	text := out.String()
	if n := strings.Count(text, "Acme Corp"); n != 3 {
		t.Errorf("want the name 3 times, got %d:\n%s", n, text)
	}
	if controls.MatchString(text) {
		t.Errorf("controls reached the terminal: %q", text)
	}
	for _, want := range []string{
		"Account authorization", "Choose Create an account in the browser, then authorize this device.", "Device code: Acme Corp",
		"Continue in your browser; this terminal will wait", "https://auth.example.test/device",
		"Complete payment setup for Acme Corp in the browser. Waiting for verified payment and provisioning.",
		"Agent configured", "Config: /home/user/.claude.json", "MCP sign-in is pending.",
		"Durin skills installed: 5. Local hooks: configured; reopen the agent to activate.",
		"Wire the generated hook command into your custom harness before tool execution.",
		"For Codex, review and trust the hook in /hooks.",
		"Setup complete", "Organization: Acme Corp", "Agent: Codex", "Durin MCP: signed in and verified", "Available tools: 0",
		"No tools are currently available. Ask your administrator to enable a connection or grant access.",
		"Reopen Codex to load Durin tools and skills. You can also use durin tools or connect an MCP client directly.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if len(opened) != 1 || opened[0] != "https://auth.example.test/device" {
		t.Errorf("opened %v", opened)
	}
}

func TestOpenBrowserOpensOnlyWebURLs(t *testing.T) {
	var ran [][]string
	original := openBrowserCommand
	openBrowserCommand = func(_ context.Context, command string, args []string) {
		ran = append(ran, append([]string{command}, args...))
	}
	defer func() { openBrowserCommand = original }()
	if err := OpenBrowser(context.Background(), "https://auth.example.test/authorize?state=one"); err != nil || len(ran) != 1 || ran[0][len(ran[0])-1] != "https://auth.example.test/authorize?state=one" {
		t.Fatalf("%v %v", ran, err)
	}
	for _, url := range []string{"file:///C:/Windows/System32/calc.exe", "smb://attacker.example.test/share", "ms-msdt:/id%20PCWDiagnostic", "javascript:alert(1)", "not a url"} {
		wantCliError(t, OpenBrowser(context.Background(), url), "Refusing to open a non-web authorization URL.")
	}
	if len(ran) != 1 {
		t.Errorf("launched %v", ran)
	}
	for goos, want := range map[string]string{"darwin": "open", "windows": "rundll32", "linux": "xdg-open", "freebsd": "xdg-open"} {
		if command, _ := browserOpener(goos, "https://x.test/"); command != want {
			t.Errorf("%s: %s", goos, command)
		}
	}
}

func TestJsNumber(t *testing.T) {
	for input, want := range map[string]float64{"": 0, " 10 ": 10, "1e3": 1000, "0x10": 16, "0b11": 3, "0o17": 15, ".5": 0.5, "5.": 5, "+7": 7, "\u00a012\u2028": 12} {
		if got := jsNumber(input); got != want {
			t.Errorf("Number(%q) = %v, want %v", input, got, want)
		}
	}
	for _, input := range []string{"10abc", "1_000", "0x", "-0x10", "inf", "NaN", "1e", "++1", "0x1p3"} {
		if got := jsNumber(input); got == got {
			t.Errorf("Number(%q) = %v, want NaN", input, got)
		}
	}
}
