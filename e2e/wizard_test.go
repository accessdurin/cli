package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Terminal drives an interactive durin run through a pseudo-terminal. Prompts are matched by
// their text, never by rendering, so any prompt library that asks the same questions works.
type Terminal struct {
	e      *Env
	file   *os.File
	mu     sync.Mutex
	output strings.Builder
	cursor int
	done   chan int
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78]`)

func (e *Env) StartTerminal(args ...string) *Terminal {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	e.t.Cleanup(cancel)
	cmd := e.command(ctx, args)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLUMNS=200", "LINES=60")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 200})
	if err != nil {
		e.t.Fatal(err)
	}
	term := &Terminal{e: e, file: file, done: make(chan int, 1)}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := file.Read(buffer)
			if n > 0 {
				term.mu.Lock()
				term.output.Write(buffer[:n])
				term.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		code := 0
		if exit, ok := err.(interface{ ExitCode() int }); ok && err != nil {
			code = exit.ExitCode()
		}
		term.done <- code
	}()
	e.t.Cleanup(func() { _ = file.Close() })
	return term
}

// plain is the output so far without escape sequences.
func (term *Terminal) plain() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return ansi.ReplaceAllString(term.output.String(), "")
}

// Expect waits until text appears after the previous match.
func (term *Terminal) Expect(text string) {
	term.e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out := term.plain()
		if i := strings.Index(out[min(term.cursor, len(out)):], text); i >= 0 {
			term.cursor += i + len(text)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	term.e.t.Fatalf("terminal never showed %q; output:\n%s", text, term.plain())
}

// Type sends keystrokes after a short pause, so the prompt is ready for them.
func (term *Terminal) Type(keys string) {
	time.Sleep(300 * time.Millisecond)
	_, _ = io.WriteString(term.file, keys)
}

const (
	keyEnter = "\r"
	keyDown  = "\x1b[B"
)

// Wait returns the exit status.
func (term *Terminal) Wait() int {
	select {
	case code := <-term.done:
		return code
	case <-time.After(2 * time.Minute):
		term.e.t.Fatalf("durin did not exit; output:\n%s", term.plain())
		return -1
	}
}

// browser installs a fake xdg-open that records each URL. Authorization URLs are "approved":
// the harness requests them and follows the redirect to the CLI's loopback callback.
func (e *Env) browser() *[]string {
	opened := filepath.Join(e.Root, "opened")
	e.Executable("xdg-open", `printf '%s\n' "$1" >> "`+opened+`"`)
	var urls []string
	var mu sync.Mutex
	stop := make(chan struct{})
	e.t.Cleanup(func() { close(stop) })
	go func() {
		seen := 0
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			data, _ := os.ReadFile(opened)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			for ; seen < len(lines) && lines[seen] != ""; seen++ {
				url := lines[seen]
				mu.Lock()
				urls = append(urls, url)
				mu.Unlock()
				if !strings.Contains(url, "/authorize?") {
					continue
				}
				response, err := client.Get(url)
				if err != nil {
					continue
				}
				response.Body.Close()
				if location := response.Header.Get("Location"); location != "" {
					if callback, err := client.Get(location); err == nil {
						callback.Body.Close()
					}
				}
			}
		}
	}()
	return &urls
}

// launcher normalization: the command that starts the installed runtime differs by
// implementation (node + runtime/cli.mjs, or a runtime executable); what follows it must not.
var launcherRules = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`"command": "[^"]*",(\s*)"args": \[(\s*)"[^"]*/runtime/cli\.mjs",\s*"`), `"command": "<LAUNCHER>",$1"args": [$2"`},
	{regexp.MustCompile(`"command": "[^"]*/runtime/durin(\.exe)?"`), `"command": "<LAUNCHER>"`},
	{regexp.MustCompile(`'[^']*' '[^']*/runtime/cli\.mjs' `), `<LAUNCHER> `},
	{regexp.MustCompile(`'[^']*/runtime/durin(\.exe)?' `), `<LAUNCHER> `},
	{regexp.MustCompile(`"assetDigest": "[0-9a-f]{64}"`), `"assetDigest": "<DIGEST>"`},
}

func normalizeLaunchers(s string) string {
	for _, rule := range launcherRules {
		s = rule.pattern.ReplaceAllString(s, rule.replacement)
	}
	return s
}

func (e *Env) noteFile(title, path string) {
	e.Note(title, normalizeLaunchers(ReadFile(path)))
}

// installedLauncher returns the bridge command of the agent entry, for running it directly.
func bridgeCommand(t *testing.T, configPath, name string) []string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	entry, ok := config.Servers[name]
	if !ok {
		t.Fatalf("no %s entry in %s", name, configPath)
	}
	return append([]string{entry.Command}, entry.Args...)
}

func TestWizard(t *testing.T) {
	e := NewEnv(t)
	requireKeyring(t)
	e.Server.Pages = [][]Tool{{{"name": "github.create_issue", "inputSchema": map[string]any{"type": "object"}}}}
	e.Server.DevicePending = 1
	e.Executable("claude", "exit 0\n")
	opened := e.browser()
	check := e.checker()
	systemctl := e.fakeSystemctl()
	descriptor := filepath.Join(e.Root, "checker.json")
	e.WriteFile(descriptor, `{"command":"`+check+`","args":["--client","claude"],"timeoutMs":4000}`, 0o600)

	term := e.StartTerminal("login", "--dev", "--app-url", e.Server.Origin(), "--config-dir", e.Config)
	term.Expect("Welcome to Durin")
	term.Expect("Authenticate your Durin account")
	term.Expect("Device code: WXYZ-1234")
	term.Expect("Which organization should this agent use?")
	term.Expect("Acme")
	term.Type(keyEnter)
	term.Expect("Which agent would you like to use?")
	term.Expect("Claude Code")
	term.Type(keyEnter)
	term.Expect("Sign in to this organization")
	term.Expect("Verify the saved agent bridge")
	term.Expect("Configure local pre-tool checks?")
	term.Type(keyEnter) // the first option is the custom checker; there is no Endpoint helper here
	term.Expect("Path to your local checker descriptor JSON")
	term.Type(descriptor + keyEnter)
	term.Expect("Report local MCP tool calls to your organization?")
	term.Type("y") // the default is no
	term.Expect("Install the Durin observer?")
	term.Type("y")
	term.Expect("Setup complete")
	term.Expect("Available tools: 1")
	code := term.Wait()
	e.Record([]string{"login --dev --app-url <ORIGIN> --config-dir <TMP>/config (interactive)"}, Result{Code: code})
	if code != 0 {
		t.Fatalf("wizard failed; output:\n%s", term.plain())
	}
	e.NoteRequests()
	e.Note("opened in the browser", strings.Join(*opened, "\n")+"\n")

	profile := e.DefaultProfile()
	profile.AccountClientID = "client_account"
	id := profile.ID()
	e.normalizer.Add(id, "<PROFILE>")
	e.normalizer.Add(id[:12], "<PROFILE12>")
	e.Note("config directory (without runtime)", treeWithout(e, e.Config, "runtime"))
	e.noteFile("profile", filepath.Join(e.Config, "profiles", id+".json"))
	e.noteFile("active", filepath.Join(e.Config, "active"))
	e.noteFile("~/.claude.json", filepath.Join(e.Home, ".claude.json"))
	e.noteFile("~/.claude/settings.json", filepath.Join(e.Home, ".claude", "settings.json"))
	e.noteFile("hook installation", filepath.Join(e.Config, "hooks", "claude.json"))
	e.noteFile("reporting installation", filepath.Join(e.Config, "reporting", "claude.json"))
	e.Note("systemctl calls", ReadFile(systemctl))
	e.Note("skills", e.Tree(filepath.Join(e.Home, ".claude", "skills")))
	e.noteFile("durin-tools skill manifest", filepath.Join(e.Home, ".claude", "skills", "durin-tools", ".durin-install.json"))
	e.Note("credentials", e.CredentialKeys("mcp:"+id))
	tokens, _ := e.Credential("mcp:" + id + ":tokens")
	e.Note("tokens", tokens)
	client, _ := e.Credential("mcp:" + id + ":client")
	e.Note("client", client)

	// The recorded digest is the hook target's content digest.
	var installation struct {
		AssetDigest string `json:"assetDigest"`
	}
	_ = json.Unmarshal([]byte(ReadFile(filepath.Join(e.Config, "hooks", "claude.json"))), &installation)
	settings := sha256.Sum256([]byte(ReadFile(filepath.Join(e.Home, ".claude", "settings.json"))))
	if installation.AssetDigest != hex.EncodeToString(settings[:]) {
		t.Errorf("hook installation digest %s does not match settings.json", installation.AssetDigest)
	}

	// The saved bridge works on its own, as the agent will start it.
	argv := bridgeCommand(t, filepath.Join(e.Home, ".claude.json"), "durin-"+id[:12])
	for i, arg := range argv {
		if arg == "mcp" {
			e.Note("bridge arguments after the launcher", strings.Join(argv[i:], " ")+"\n")
			break
		}
	}
	e.Run("", "hooks", "status", "--config-dir", e.Config)
	e.Run("", "verify", "--config-dir", e.Config)
	e.Server.ResetLog()

	// Running setup again changes nothing and needs no browser: the grant is refreshed.
	before := ReadFile(filepath.Join(e.Home, ".claude.json"))
	serviceCalls := ReadFile(systemctl)
	term = e.StartTerminal("login", "--dev", "--app-url", e.Server.Origin(), "--config-dir", e.Config)
	term.Expect("Which organization should this agent use?")
	term.Type(keyEnter)
	term.Expect("Which agent would you like to use?")
	term.Type(keyEnter)
	term.Expect("Configure local pre-tool checks?")
	term.Type(keyDown + keyEnter) // skip
	term.Expect("Report local MCP tool calls to your organization?")
	term.Type("y")
	term.Expect("Install the Durin observer?")
	term.Type(keyEnter) // no, the default
	term.Expect("Durin observer: not installed; turn it on later with durin observer install.")
	term.Expect("Setup complete")
	code = term.Wait()
	e.Record([]string{"login (second run, interactive)"}, Result{Code: code})
	if ReadFile(systemctl) != serviceCalls {
		t.Error("declining the observer still called the service manager")
	}
	e.NoteRequests()
	if after := ReadFile(filepath.Join(e.Home, ".claude.json")); after != before {
		e.Note("~/.claude.json changed on the second run", normalizeLaunchers(after))
	}
	e.Note("config directory after second run (without runtime)", treeWithout(e, e.Config, "runtime"))
	e.Note("backups in home", backups(e.Home))
}

// A conflicting agent entry is preserved and reported; nothing else is written.
func TestWizardConflict(t *testing.T) {
	e := NewEnv(t)
	requireKeyring(t)
	e.Executable("claude", "exit 0\n")
	e.browser()
	profile := e.DefaultProfile()
	id := profile.ID()
	e.normalizer.Add(id, "<PROFILE>")
	e.normalizer.Add(id[:12], "<PROFILE12>")
	original := "{\n  // keep me\n  \"mcpServers\": {\n    \"durin-" + id[:12] + "\": { \"type\": \"stdio\", \"command\": \"/somewhere/else\", \"args\": [] }\n  }\n}\n"
	e.WriteFile(filepath.Join(e.Home, ".claude.json"), original, 0o600)

	term := e.StartTerminal("login", "--dev", "--app-url", e.Server.Origin(), "--config-dir", e.Config)
	term.Expect("Which organization should this agent use?")
	term.Type(keyEnter)
	term.Expect("Which agent would you like to use?")
	term.Type(keyEnter)
	code := term.Wait()
	e.Record([]string{"login (conflict, interactive)"}, Result{Code: code})
	out := term.plain()
	const message = "This organization already has a conflicting agent configuration. Review it before running durin again."
	e.Note("reported conflict", map[bool]string{true: "yes\n", false: "no\n"}[strings.Contains(out, message)])
	e.noteFile("~/.claude.json", filepath.Join(e.Home, ".claude.json"))
	e.Note("config directory (without runtime)", treeWithout(e, e.Config, "runtime"))
	e.NoteRequests()

	// An agent that is not installed stops setup with an explanation.
	_ = os.Remove(filepath.Join(e.Bin, "claude"))
	term = e.StartTerminal("login", "--dev", "--app-url", e.Server.Origin(), "--config-dir", e.Config)
	term.Expect("Which organization should this agent use?")
	term.Type(keyEnter)
	term.Expect("Which agent would you like to use?")
	term.Expect("not detected; install before continuing")
	term.Type(keyEnter)
	term.Expect("Install Claude Code, then run durin to resume. Your organization answers are saved.")
	code = term.Wait()
	e.Record([]string{"login (agent missing, interactive)"}, Result{Code: code})
	e.Server.ResetLog()
}

func treeWithout(e *Env, dir, skip string) string {
	var lines []string
	for _, line := range strings.Split(e.Tree(dir), "\n") {
		if line == "" || line == skip+" dir 0700" || strings.HasPrefix(line, skip+"/") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n") + "\n"
}

func backups(home string) string {
	var lines []string
	_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
		if err == nil && strings.Contains(filepath.Base(path), ".durin-backup-") {
			rel, _ := filepath.Rel(home, path)
			lines = append(lines, uuidPattern.ReplaceAllString(rel, "<UUID>"))
		}
		return nil
	})
	return strings.Join(lines, "\n") + "\n"
}
