// Package e2e is a black-box parity harness for the durin CLI. It runs whichever build
// DURIN_BIN names (by default a fresh `go build` of ./cmd/durin) against fake durin, WorkOS,
// OAuth and MCP servers on loopback, with a real OS credential store, and compares each
// scenario's normalized transcript with a golden file. The goldens were recorded from the
// TypeScript CLI this Go CLI replaced (see docs/go-port.md), so a build that passes behaves
// like it: same output, exit codes, files and HTTP requests.
//
// Run with e2e/run.sh, which starts a private D-Bus session with an unlocked keyring.
// Set DURIN_UPDATE_GOLDEN=1 to re-record goldens.
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Env is one isolated user: a home directory, a durin configuration directory and a PATH
// directory for fake executables (agents, the browser opener, checkers).
type Env struct {
	t      *testing.T
	Root   string
	Home   string
	Config string
	Bin    string
	Server *FakeServer
	Extra  []string // extra environment entries for every run

	transcript strings.Builder
	normalizer *Normalizer
}

func durinCommand(t *testing.T) []string {
	t.Helper()
	command := strings.Fields(os.Getenv("DURIN_BIN"))
	if len(command) == 0 {
		t.Skip("DURIN_BIN is not set; run e2e/run.sh")
	}
	return command
}

func NewEnv(t *testing.T) *Env {
	t.Helper()
	durinCommand(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{t: t, Root: root, Home: filepath.Join(root, "home"), Config: filepath.Join(root, "config"), Bin: filepath.Join(root, "bin")}
	for _, dir := range []string{e.Home, e.Config, e.Bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.Server = NewFakeServer(t)
	e.normalizer = NewNormalizer(root, e.Server.Origin())
	t.Cleanup(func() { e.checkGolden() })
	return e
}

// Result is one finished CLI run.
type Result struct {
	Code   int
	Stdout string
	Stderr string
}

// environment is what every run gets. DO_NOT_TRACK keeps telemetry off.
func (e *Env) environment() []string {
	env := []string{
		"HOME=" + e.Home,
		"PATH=" + e.Bin + string(os.PathListSeparator) + "/usr/local/bin:/usr/bin:/bin",
		"DO_NOT_TRACK=1",
		"LANG=C.UTF-8",
	}
	for _, key := range []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, e.Extra...)
}

func (e *Env) command(ctx context.Context, args []string) *exec.Cmd {
	base := durinCommand(e.t)
	cmd := exec.CommandContext(ctx, base[0], append(base[1:], args...)...)
	cmd.Env = e.environment()
	cmd.Dir = e.Root
	return cmd
}

// Run runs durin with args and stdin, records it in the transcript and returns the result.
func (e *Env) Run(stdin string, args ...string) Result {
	e.t.Helper()
	result := e.runQuiet(stdin, args...)
	e.Record(args, result)
	return result
}

func (e *Env) runQuiet(stdin string, args ...string) Result {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := e.command(ctx, args)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		e.t.Fatalf("durin %v: %v", args, err)
	}
	return Result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// RunSortedList runs a command whose stdout is a JSON array in no defined order (it follows
// directory order) and records it sorted by the given field.
func (e *Env) RunSortedList(field string, args ...string) Result {
	e.t.Helper()
	result := e.runQuiet("", args...)
	var items []map[string]any
	if json.Unmarshal([]byte(result.Stdout), &items) == nil && len(items) > 1 {
		sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i][field]) < fmt.Sprint(items[j][field]) })
		out, _ := json.MarshalIndent(items, "", "  ")
		result.Stdout = "(sorted by " + field + " for comparison; keys sorted)\n" + string(out) + "\n"
	}
	e.Record(args, result)
	return result
}

// Record appends a run to the transcript.
func (e *Env) Record(args []string, result Result) {
	fmt.Fprintf(&e.transcript, "## durin %s\nexit: %d\n--- stdout\n%s--- stderr\n%s", strings.Join(args, " "), result.Code, ensureNewline(result.Stdout), ensureNewline(result.Stderr))
}

// Note adds free-form, already normalized observations (files, keyring entries) to the transcript.
func (e *Env) Note(title, body string) {
	fmt.Fprintf(&e.transcript, "## %s\n%s", title, ensureNewline(body))
}

// NoteRequests adds the fake server's request log and clears it.
func (e *Env) NoteRequests() {
	var b strings.Builder
	var notifications []string
	for _, r := range e.Server.Requests() {
		// The SDK opens the standalone event stream asynchronously after initialization, so
		// where (and whether, for a short command) it lands in the log is timing, not behaviour.
		if r.Method == "GET" && strings.HasPrefix(r.Path, "/mcp/u/") {
			continue
		}
		line := r.Method + " " + r.Path
		if r.Query != "" {
			line += "?" + r.Query
		}
		keys := make([]string, 0, len(r.Headers))
		for k := range r.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line += fmt.Sprintf(" [%s: %s]", k, r.Headers[k])
		}
		if r.Body != "" {
			line += " " + r.Body
		}
		// A bridge forwards each message under the profile lock, which it polls for; a
		// notification can therefore overtake or trail the next request. Notifications are
		// listed separately and sorted.
		if strings.Contains(r.Body, `"method":"notifications/`) && !strings.Contains(r.Body, `"id":`) {
			notifications = append(notifications, line)
			continue
		}
		b.WriteString(line + "\n")
	}
	// A bridge that is closed while a notification still waits for the lock never sends it,
	// so only the distinct notifications are compared, not how often each arrived.
	sort.Strings(notifications)
	for i, line := range notifications {
		if i > 0 && notifications[i-1] == line {
			continue
		}
		b.WriteString("(notification) " + line + "\n")
	}
	e.Server.ResetLog()
	e.Note("requests", b.String())
}

func ensureNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n⏎ (no trailing newline)\n"
}

func (e *Env) checkGolden() {
	if e.t.Failed() || e.t.Skipped() {
		return
	}
	name := regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(e.t.Name(), "_")
	path := filepath.Join("testdata", "golden", name+".txt")
	got := e.normalizer.Apply(e.transcript.String())
	if os.Getenv("DURIN_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			e.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("missing golden %s (record with DURIN_UPDATE_GOLDEN=1): %v", path, err)
	}
	if string(want) != got {
		e.t.Errorf("transcript differs from %s:\n%s", path, lineDiff(string(want), got))
	}
}

// lineDiff shows the first differing lines with context; enough to locate a divergence.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			start := max(0, i-3)
			var b strings.Builder
			for j := start; j < i; j++ {
				if j < len(w) {
					fmt.Fprintf(&b, "  %s\n", w[j])
				}
			}
			fmt.Fprintf(&b, "- %s\n+ %s\n", wl, gl)
			for j := i + 1; j < i+8 && (j < len(w) || j < len(g)); j++ {
				if j < len(w) {
					fmt.Fprintf(&b, "- %s\n", w[j])
				}
				if j < len(g) {
					fmt.Fprintf(&b, "+ %s\n", g[j])
				}
			}
			return fmt.Sprintf("line %d:\n%s", i+1, b.String())
		}
	}
	return "(identical lines; differs in trailing bytes)"
}

// Normalizer replaces values that legitimately differ between runs: temporary paths, ports,
// generated identifiers and timestamps. Each distinct UUID gets a stable numbered name, so a
// transcript still shows when two places carry the same identifier.
type Normalizer struct {
	replacements [][2]string
	uuids        map[string]string
}

func NewNormalizer(root, origin string) *Normalizer {
	return &Normalizer{replacements: [][2]string{{root, "<TMP>"}, {origin, "<ORIGIN>"}}, uuids: map[string]string{}}
}

// Add registers one more literal replacement (applied before the generic patterns).
func (n *Normalizer) Add(value, name string) {
	n.replacements = append(n.replacements, [2]string{value, name})
}

var (
	uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	datePattern = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z`)
	portPattern = regexp.MustCompile(`(127\.0\.0\.1|localhost)(:|%3A)\d{2,5}`)
	// PKCE values and OAuth state are random by design.
	randomParam = regexp.MustCompile(`\b(code_challenge|code_verifier|state)=[A-Za-z0-9_.~-]+`)
)

func (n *Normalizer) Apply(s string) string {
	// Longest literals first, so a path inside another path is replaced whole.
	sorted := append([][2]string(nil), n.replacements...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i][0]) > len(sorted[j][0]) })
	for _, r := range sorted {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	s = uuidPattern.ReplaceAllStringFunc(s, func(id string) string {
		if name, ok := n.uuids[id]; ok {
			return name
		}
		name := fmt.Sprintf("<UUID-%d>", len(n.uuids)+1)
		n.uuids[id] = name
		return name
	})
	s = datePattern.ReplaceAllString(s, "<DATE>")
	s = randomParam.ReplaceAllString(s, "$1=<RANDOM>")
	return portPattern.ReplaceAllString(s, "$1$2<PORT>")
}

// --- Profiles and credentials ------------------------------------------------------------

// Profile mirrors a saved durin profile. Field order is the schema's, which determines the id.
type Profile struct {
	AppOrigin            string `json:"appOrigin"`
	AccountIssuer        string `json:"accountIssuer"`
	AccountClientID      string `json:"accountClientId"`
	PrincipalID          string `json:"principalId"`
	WorkosOrganizationID string `json:"workosOrganizationId"`
	OrganizationID       string `json:"organizationId"`
	Environment          string `json:"environment"`
	Resource             string `json:"resource"`
	McpIssuer            string `json:"mcpIssuer"`
	Development          bool   `json:"development"`
}

// ID is sha256 of JSON.stringify(scope), as the CLI computes it.
func (p Profile) ID() string {
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DefaultProfile is a development profile bound to the fake server.
func (e *Env) DefaultProfile() Profile {
	return Profile{
		AppOrigin: e.Server.Origin(), AccountIssuer: e.Server.Origin(), AccountClientID: "client_account",
		PrincipalID: "user_01", WorkosOrganizationID: "org_wos_1", OrganizationID: "org_1",
		Environment: "production", Resource: e.Server.Resource(), McpIssuer: e.Server.Origin(), Development: true,
	}
}

// SaveProfile writes a profile file exactly as the CLI saves it and returns its id.
func (e *Env) SaveProfile(p Profile) string {
	e.t.Helper()
	id := p.ID()
	scope, _ := json.Marshal(p)
	// {...scope, version: 1, id}, pretty-printed with two spaces.
	var ordered bytes.Buffer
	ordered.Write(scope[:len(scope)-1])
	fmt.Fprintf(&ordered, `,"version":1,"id":%q}`, id)
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, ordered.Bytes(), "", "  "); err != nil {
		e.t.Fatal(err)
	}
	pretty.WriteByte('\n')
	e.WriteFile(filepath.Join(e.Config, "profiles", id+".json"), pretty.String(), 0o600)
	e.normalizer.Add(id, "<PROFILE:"+p.PrincipalID+">")
	e.normalizer.Add(id[:12], "<PROFILE12:"+p.PrincipalID+">")
	return id
}

func (e *Env) WriteFile(path, content string, mode os.FileMode) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		e.t.Fatal(err)
	}
}

// Executable writes a shell script into the fake PATH directory (or elsewhere when path is absolute).
func (e *Env) Executable(name, script string) string {
	e.t.Helper()
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Bin, name)
	}
	e.WriteFile(path, "#!/bin/sh\n"+script, 0o700)
	return path
}

// --- OS credential store (secret-service) ----------------------------------------------------

const credentialService = "com.getdurin.cli"

func requireKeyring(t *testing.T) {
	t.Helper()
	if os.Getenv("DURIN_E2E_KEYRING") != "1" {
		t.Skip("needs the private keyring session from e2e/run.sh")
	}
}

// StoreCredential writes an entry the way the CLI's credential store does.
func (e *Env) StoreCredential(key, value string) {
	e.t.Helper()
	requireKeyring(e.t)
	cmd := exec.Command("secret-tool", "store", "--label=keyring:"+key+"@"+credentialService, "service", credentialService, "username", key)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("secret-tool store %s: %v %s", key, err, out)
	}
	e.t.Cleanup(func() { _ = exec.Command("secret-tool", "clear", "service", credentialService, "username", key).Run() })
}

// Credential reads an entry; found is false when it is absent.
func (e *Env) Credential(key string) (string, bool) {
	e.t.Helper()
	requireKeyring(e.t)
	out, err := exec.Command("secret-tool", "lookup", "service", credentialService, "username", key).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// CredentialKeys lists the store entries whose key starts with prefix, with their labels.
func (e *Env) CredentialKeys(prefix string) string {
	e.t.Helper()
	requireKeyring(e.t)
	out, _ := exec.Command("secret-tool", "search", "--all", "service", credentialService).CombinedOutput()
	var lines []string
	var label string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "label = "):
			label = strings.TrimPrefix(line, "label = ")
		case strings.HasPrefix(line, "attribute.username = "):
			key := strings.TrimPrefix(line, "attribute.username = ")
			if strings.HasPrefix(key, prefix) {
				lines = append(lines, key+" (label "+label+")")
			}
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// SeedGrant stores an MCP client registration and tokens for a profile, as a finished sign-in leaves them.
func (e *Env) SeedGrant(id, access, refresh string) {
	e.t.Helper()
	issuer := e.Server.Origin()
	e.StoreCredential("mcp:"+id+":client", fmt.Sprintf(`{"client_id":"client-seeded","redirect_uris":["http://127.0.0.1:1/oauth/callback"],"token_endpoint_auth_method":"none","issuer":%q}`, issuer))
	tokens := map[string]any{"access_token": access, "token_type": "Bearer", "issuer": issuer}
	if refresh != "" {
		tokens["refresh_token"] = refresh
	}
	data, _ := json.Marshal(tokens)
	e.StoreCredential("mcp:"+id+":tokens", string(data))
	e.Server.AcceptedTokens[access] = true
}

// Tree lists files under dir (relative paths, with mode), normalized and then sorted, so
// generated names such as profile ids sort the same way in every run.
func (e *Env) Tree(dir string) string {
	lines := strings.Split(strings.TrimSuffix(e.normalizer.Apply(rawTree(dir)), "\n"), "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func rawTree(dir string) string {
	var lines []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		kind := "file"
		if info.IsDir() {
			kind = "dir"
		}
		lines = append(lines, fmt.Sprintf("%s %s %04o", rel, kind, info.Mode().Perm()))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

// ReadFile returns a file's content or "<missing>".
func ReadFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "<missing>\n"
	}
	return string(data)
}
