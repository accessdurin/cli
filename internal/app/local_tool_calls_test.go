package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
)

const reportProfile = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

var reportBridgeEntry = `{"command":"/opt/durin/runtime/durin","args":["mcp","serve","--profile","` + reportProfile + `","--config-dir","/x"]}`

func parsed(t *testing.T, raw string) jsjson.Value {
	t.Helper()
	v, err := jsjson.ParseString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func claudeEvent(changes string) string {
	base := `"hook_event_name":"PostToolUse","session_id":"session-1","prompt_id":"prompt-9","tool_use_id":"toolu_01",` +
		`"tool_name":"mcp__filesystem__read_file","tool_input":{"path":"/home/dev/notes.md"},` +
		`"tool_response":{"content":[{"type":"text","text":"PRIVATE RESULT"}]},"cwd":"/home/dev/project"`
	if changes == "" {
		return "{" + base + "}"
	}
	// Later keys win when the event is parsed, as with an object spread.
	return "{" + base + "," + changes + "}"
}

func observed(t *testing.T, format, raw string) *ObservedCall {
	t.Helper()
	call, err := ObservedMcpCall(format, parsed(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestPostToolEvents(t *testing.T) {
	call := observed(t, "claude", claudeEvent(""))
	if len(call.Targets) != 1 || call.Targets[0] != (McpTarget{"filesystem", "read_file"}) || call.TurnID != "prompt-9" || call.Outcome != "succeeded" {
		t.Fatalf("claude: %+v", call)
	}
	if call := observed(t, "claude", claudeEvent(`"hook_event_name":"PostToolUseFailure"`)); call.Outcome != "failed" || call.ToolCallID != "toolu_01" {
		t.Fatalf("claude failure: %+v", call)
	}
	if call := observed(t, "codex", claudeEvent(`"turn_id":"turn-3","tool_response":{"isError":true}`)); call.TurnID != "turn-3" || call.Outcome != "failed" {
		t.Fatalf("codex: %+v", call)
	}
	cursor := observed(t, "cursor", `{"conversation_id":"conversation","generation_id":"generation","tool_name":"MCP:search",`+
		`"tool_input":"{\"query\":\"durin\"}","mcp_server_name":"docs","result_json":"{\"content\":[]}","workspace_roots":["/repo"]}`)
	if cursor.Targets[0] != (McpTarget{"docs", "search"}) || jsjson.Stringify(cursor.Input) != `{"query":"durin"}` || cursor.Cwd != "/repo" || cursor.Outcome != "succeeded" {
		t.Fatalf("cursor: %+v", cursor)
	}
	windsurf := observed(t, "windsurf", `{"agent_action_name":"post_mcp_tool_use","trajectory_id":"trajectory","execution_id":"execution",`+
		`"tool_info":{"mcp_server_name":"db","mcp_tool_name":"query","mcp_tool_arguments":{"q":1}}}`)
	if windsurf.Targets[0] != (McpTarget{"db", "query"}) || windsurf.Outcome != "unknown" || windsurf.SessionID != "trajectory" || windsurf.TurnID != "execution" {
		t.Fatalf("windsurf: %+v", windsurf)
	}
}

func TestBuiltInToolsAndDoubleUnderscoreNames(t *testing.T) {
	if call := observed(t, "claude", claudeEvent(`"tool_name":"Bash"`)); call != nil {
		t.Fatalf("a built-in tool is not an MCP call: %+v", call)
	}
	got := McpTargets("mcp__a__b__c")
	want := []McpTarget{{"a", "b__c"}, {"a__b", "c"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("splits = %+v", got)
	}
}

func TestLocalServerClassification(t *testing.T) {
	cases := map[string]string{
		`{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem"]}`:    "stdio",
		`{"command":["uvx","mcp-server-git"]}`:                                         "stdio",
		`{"type":"http","url":"http://127.0.0.1:3845/mcp"}`:                            "loopback_http",
		`{"serverUrl":"http://localhost:8080/sse"}`:                                    "loopback_http",
		`{"url":"http://[::1]:9000/mcp"}`:                                              "loopback_http",
		`{"url":"https://mcp.example.com/mcp"}`:                                        "",
		`{"url":"http://127.0.0.1.example.com/mcp"}`:                                   "",
		`{"command":"npx","disabled":true}`:                                            "",
		reportBridgeEntry:                                                              "",
		`{"command":"durin","args":["mcp","serve","--profile=` + reportProfile + `"]}`: "",
		`{}`: "",
	}
	for entry, want := range cases {
		if got := entryTransport(parsed(t, entry)); got != want {
			t.Errorf("%s: transport %q, want %q", entry, got, want)
		}
	}
	if loopbackURL("not a url") {
		t.Error("a malformed URL is not loopback")
	}
}

func TestClaudePrecedenceFromASubfolder(t *testing.T) {
	root := tempDir(t)
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, "packages", "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := AgentsFor(root, EnvMap{})["claude"]
	writeFile(t, agent.ConfigPath, `{"mcpServers":{"filesystem":{"command":"npx"},"shadowed":{"command":"npx"},`+
		`"my.files":{"command":"node"},"a__b":{"command":"node"},"durin-bbbbbbbbbbbb":`+reportBridgeEntry+`},`+
		`"projects":{`+strconv.Quote(project)+`:{"mcpServers":{"scoped":{"url":"http://localhost:1/mcp"}}}}}`)
	writeFile(t, filepath.Join(project, ".mcp.json"), `{"mcpServers":{"team":{"command":"node"},"shadowed":{"url":"https://remote.example/mcp"}}}`)
	location := McpConfigLocation{ConfigPath: agent.ConfigPath, Format: agent.Format, ServerKey: agent.ServerKey}
	lookup := func(name string) *LocalServer {
		return localServerFor("claude", location, McpTargets(name), filepath.Join(project, "packages", "api"))
	}
	if got := lookup("mcp__filesystem__read"); got == nil || *got != (LocalServer{"stdio", "filesystem", "read"}) {
		t.Errorf("filesystem: %+v", got)
	}
	if got := lookup("mcp__scoped__q"); got == nil || got.Transport != "loopback_http" {
		t.Errorf("scoped: %+v", got)
	}
	if got := lookup("mcp__team__q"); got == nil || got.Transport != "stdio" {
		t.Errorf("team: %+v", got)
	}
	if got := lookup("mcp__my_files__read"); got == nil || got.Server != "my.files" {
		t.Errorf("normalized name: %+v", got)
	}
	if got := lookup("mcp__a__b__run"); got == nil || got.Server != "a__b" || got.Tool != "run" {
		t.Errorf("double underscore: %+v", got)
	}
	for _, name := range []string{"mcp__shadowed__q", "mcp__durin-bbbbbbbbbbbb__durin_call_tool", "mcp__plugin_tools_db__q"} {
		if got := lookup(name); got != nil {
			t.Errorf("%s must not be local: %+v", name, got)
		}
	}
}

var stdioServer = LocalServer{Transport: "stdio", Server: "filesystem", Tool: "read_file"}

func TestReportedCalls(t *testing.T) {
	call := observed(t, "claude", claudeEvent(""))
	now := time.Now()
	first, second := LocalToolCall("claude", *call, stdioServer, now), LocalToolCall("claude", *call, stdioServer, now)
	id, _ := first.GetString("reportId")
	again, _ := second.GetString("reportId")
	if id != again || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-`).MatchString(id) {
		t.Fatalf("report IDs %q %q", id, again)
	}
	if args, _ := first.GetString("argumentsJson"); args != `{"path":"/home/dev/notes.md"}` {
		t.Fatalf("arguments %s", args)
	}
	if strings.Contains(jsjson.Stringify(first), "PRIVATE RESULT") {
		t.Fatal("a report never carries the tool's result")
	}

	large := *call
	large.Input = parsed(t, `{"blob":"`+strings.Repeat("x", 20_000)+`"}`)
	report := LocalToolCall("claude", large, stdioServer, now)
	if args, _ := report.GetString("argumentsJson"); args != "{}" || !jsjson.Equal(get(report, "argumentsTruncated"), jsjson.Bool(true)) {
		t.Fatalf("large arguments: %s", jsjson.Stringify(report))
	}
	wrapped := *call
	wrapped.Input, wrapped.SessionID = parsed(t, `["a"]`), "has space"
	report = LocalToolCall("claude", wrapped, stdioServer, now)
	if args, _ := report.GetString("argumentsJson"); args != `{"value":["a"]}` || !jsjson.Equal(get(report, "sessionId"), jsjson.Null{}) {
		t.Fatalf("wrapped: %s", jsjson.Stringify(report))
	}
	bad := stdioServer
	bad.Server = `quote"server`
	if LocalToolCall("claude", *call, bad, now) != nil {
		t.Fatal("a server name Durin refuses yields no report")
	}
}

type reportingFixture struct {
	root    string
	bridge  BridgeConfiguration
	agent   contracts.AgentConfig
	flushed []string
	spool   string
}

func newReportingFixture(t *testing.T) *reportingFixture {
	t.Helper()
	root := tempDir(t)
	agent := AgentsFor(root, EnvMap{})["claude"]
	f := &reportingFixture{
		root:   root,
		agent:  agent,
		bridge: BridgeConfiguration{ExecutablePath: filepath.Join(root, "runtime", "durin"), ConfigDirectory: filepath.Join(root, "durin"), ProfileID: reportProfile},
	}
	writeFile(t, agent.ConfigPath, `{"mcpServers":{"filesystem":{"command":"npx"}}}`)
	writeFile(t, filepath.Join(f.bridge.ConfigDirectory, "profiles", reportProfile+".json"), "{}")
	if err := InstallReporting(agent, f.bridge); err != nil {
		t.Fatal(err)
	}
	f.spool = SpoolDirectory(f.bridge.ConfigDirectory, reportProfile)
	return f
}

func (f *reportingFixture) run(t *testing.T, event string, deps ReportDependencies) string {
	t.Helper()
	deps.Flush = func(directory, profile string) { f.flushed = append(f.flushed, directory+" "+profile) }
	outcome, err := ReportHook(f.bridge.ConfigDirectory, "claude", strings.NewReader(event), deps)
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func noEndpoint(string, ReportingInstallation) bool { return false }

func spooled(t *testing.T, spool string) []string {
	t.Helper()
	entries, _ := os.ReadDir(spool)
	var out []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), ".") {
			out = append(out, entry.Name())
		}
	}
	return out
}

func TestReportHookQueuesLocalCallsOnce(t *testing.T) {
	f := newReportingFixture(t)
	deps := ReportDependencies{EndpointPresent: noEndpoint}
	for range 2 {
		if got := f.run(t, claudeEvent(""), deps); got != ReportQueued {
			t.Fatalf("outcome %s", got)
		}
	}
	files := spooled(t, f.spool)
	if len(files) != 1 {
		t.Fatalf("spool %v", files)
	}
	call := parsed(t, readFile(t, filepath.Join(f.spool, files[0]))).(*jsjson.Object)
	for key, want := range map[string]string{"reporter": "cli", "agent": "claude", "mcpServer": "filesystem", "transport": "stdio"} {
		if got, _ := call.GetString(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if len(f.flushed) != 2 || f.flushed[0] != f.bridge.ConfigDirectory+" "+reportProfile {
		t.Fatalf("flushes %v", f.flushed)
	}
}

func TestReportHookStaysQuiet(t *testing.T) {
	f := newReportingFixture(t)
	if got := f.run(t, claudeEvent(""), ReportDependencies{EndpointPresent: func(string, ReportingInstallation) bool { return true }}); got != ReportEndpoint {
		t.Fatalf("endpoint: %s", got)
	}
	deps := ReportDependencies{EndpointPresent: noEndpoint}
	if got := f.run(t, claudeEvent(`"tool_name":"mcp__remote__search"`), deps); got != ReportNotLocal {
		t.Fatalf("remote: %s", got)
	}
	if got := f.run(t, claudeEvent(`"tool_name":"Read"`), deps); got != ReportIgnored {
		t.Fatalf("built-in: %s", got)
	}
	marker, err := NewFileRevocationMarker(f.bridge.ConfigDirectory, reportProfile)
	if err != nil || marker.Mark() != nil {
		t.Fatal(err)
	}
	if got := f.run(t, claudeEvent(""), deps); got != ReportSignedOut {
		t.Fatalf("unfinished sign-out: %s", got)
	}
	if err := errors.Join(marker.Clear(), os.Remove(filepath.Join(f.bridge.ConfigDirectory, "profiles", reportProfile+".json"))); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, claudeEvent(""), deps); got != ReportSignedOut {
		t.Fatalf("removed profile: %s", got)
	}
	if files := spooled(t, f.spool); len(files) != 0 || len(f.flushed) != 0 {
		t.Fatalf("nothing is queued or flushed: %v %v", files, f.flushed)
	}
}

func TestReportHookDefersToEndpointOnlyWhileWiredAndSignedIn(t *testing.T) {
	f := newReportingFixture(t)
	helper := "/Applications/Durin Endpoint Client.app/Contents/Helpers/observer-preflight"
	previous := endpointCheckerFor
	endpointCheckerFor = func(string) *contracts.HookChecker {
		return &contracts.HookChecker{Command: helper, Args: []string{}, TimeoutMs: 1000}
	}
	t.Cleanup(func() { endpointCheckerFor = previous })
	deps := ReportDependencies{Home: f.root}
	if got := f.run(t, claudeEvent(""), deps); got != ReportQueued {
		t.Fatalf("not wired, not signed in: %s", got)
	}
	marker := EndpointReportingMarker(f.root)
	writeFile(t, marker, `{"signedIn":true}`)
	if got := f.run(t, claudeEvent(""), deps); got != ReportQueued {
		t.Fatalf("signed in but not wired: %s", got)
	}
	hooks := f.agent.HookConfig.Path
	original := readFile(t, hooks)
	writeFile(t, hooks, strings.Replace(original, `"hooks": {`,
		`"hooks": { "PreToolUse": [{ "matcher": ".*", "hooks": [{ "type": "command", "command": "'`+helper+`' --client claude" }] }],`, 1))
	if got := f.run(t, claudeEvent(""), deps); got != ReportEndpoint {
		t.Fatalf("wired and signed in: %s", got)
	}
	stale := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}
	if got := f.run(t, claudeEvent(""), deps); got != ReportQueued {
		t.Fatalf("stale marker: %s", got)
	}
}

func TestReportHookSkipsCallsFromHarnessesThatImportClaudeHooks(t *testing.T) {
	cases := []struct {
		name, changes string
		environment   map[string]string
	}{
		{"cursor", `"cursor_version":"2.9.0","conversation_id":"cursor-conversation"`, nil},
		{"grok", `"hookEventName":"post_tool_use","sessionId":"session-1"`, nil},
		{"devin", "", map[string]string{"DEVIN_PROJECT_DIR": "/home/dev/project"}},
	}
	endpoint := map[string]func(string, ReportingInstallation) bool{
		"without endpoint": noEndpoint,
		"with endpoint":    func(string, ReportingInstallation) bool { return true },
	}
	for _, c := range cases {
		for coverage, present := range endpoint {
			f := newReportingFixture(t)
			lookup := func(name string) (string, bool) { value, found := c.environment[name]; return value, found }
			got := f.run(t, claudeEvent(c.changes), ReportDependencies{EndpointPresent: present, LookupEnv: lookup})
			if got == ReportQueued || len(spooled(t, f.spool)) != 0 || len(f.flushed) != 0 {
				t.Errorf("%s %s: outcome %s, spool %v, flushes %v", c.name, coverage, got, spooled(t, f.spool), f.flushed)
			}
		}
	}
	f := newReportingFixture(t)
	absent := func(string) (string, bool) { return "", false }
	if got := f.run(t, claudeEvent(""), ReportDependencies{EndpointPresent: noEndpoint, LookupEnv: absent}); got != ReportQueued {
		t.Fatalf("Claude Code's own call: %s", got)
	}
}

func TestReportHookDoesNothingUntilInstalled(t *testing.T) {
	outcome, err := ReportHook(tempDir(t), "codex", strings.NewReader("{}"), ReportDependencies{EndpointPresent: noEndpoint, Flush: func(string, string) {}})
	if err != nil || outcome != ReportDisabled {
		t.Fatalf("outcome %s %v", outcome, err)
	}
}

// fakeCaller records calls and answers isError when rejecting.
type fakeCaller struct {
	sent      []contracts.CliToolCall
	rejecting bool
	during    func()
}

func (c *fakeCaller) CallTool(_ context.Context, input contracts.CliToolCall) (contracts.McpToolResult, error) {
	c.sent = append(c.sent, input)
	if c.during != nil {
		c.during()
	}
	return jsjson.Obj(jsjson.KV("content", jsjson.Array{}), jsjson.KV("isError", jsjson.Bool(c.rejecting))), nil
}

func connecting(caller ToolCaller, connections *int) SpoolConnect {
	return func(work func(client ToolCaller) error) error {
		*connections++
		return work(caller)
	}
}

func spoolReport(t *testing.T, index int) *jsjson.Object {
	t.Helper()
	call := *observed(t, "claude", claudeEvent(""))
	call.ToolCallID = "call-" + strings.Repeat("x", index%3) + string(rune('a'+index%26)) + string(rune('a'+index/26))
	return LocalToolCall("claude", call, stdioServer, time.Now())
}

func TestFlushDeletesTheSpoolOfASignedOutProfile(t *testing.T) {
	dir := tempDir(t)
	profile := mustProfile(t, testScope)
	profiles := NewFileProfiles(dir)
	if err := errors.Join(profiles.Save(profile), profiles.Activate(profile.ID)); err != nil {
		t.Fatal(err)
	}
	spool := SpoolDirectory(dir, profile.ID)
	if err := spoolCall(spool, spoolReport(t, 0), time.Now); err != nil {
		t.Fatal(err)
	}
	if err := flushProfile(context.Background(), cliArgs(t, "hooks", "flush", "--config-dir", dir), newMemoryStore()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool left after sign-out: %v", err)
	}
}

func TestSpoolDeliversInShortSessionsAndBacksOff(t *testing.T) {
	spool := filepath.Join(tempDir(t), "spool")
	for i := range 12 {
		if err := spoolCall(spool, spoolReport(t, i), time.Now); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	clock := func() time.Time { return now }
	offline := errors.New("offline")
	if _, err := FlushSpool(context.Background(), spool, func(func(ToolCaller) error) error { return offline }, clock); !errors.Is(err, offline) {
		t.Fatalf("offline flush: %v", err)
	}
	if files := spooled(t, spool); len(files) != 12 {
		t.Fatalf("calls are kept after a failure: %d", len(files))
	}
	caller, connections := &fakeCaller{}, 0
	if sent, _ := FlushSpool(context.Background(), spool, connecting(caller, &connections), clock); sent != 0 {
		t.Fatalf("a flush during the backoff sends nothing: %d", sent)
	}
	now = now.Add(SpoolLimits.Backoff + time.Millisecond)
	sent, err := FlushSpool(context.Background(), spool, connecting(caller, &connections), clock)
	if err != nil || sent != 12 || connections != 3 {
		t.Fatalf("sent %d over %d connections: %v", sent, connections, err)
	}
	first := caller.sent[0]
	call, _ := first.Arguments.GetObject("call")
	if first.Name != "durin_report_local_tool_call" || !jsjson.Equal(get(call, "reporter"), jsjson.String("cli")) ||
		!regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(first.IdempotencyKey) {
		t.Fatalf("delivered %+v", first)
	}
	if files := spooled(t, spool); len(files) != 0 {
		t.Fatalf("spool not empty: %v", files)
	}
}

func TestSpoolPicksUpQueuedCallsAndDropsUnreadableFiles(t *testing.T) {
	spool := filepath.Join(tempDir(t), "spool")
	if err := spoolCall(spool, spoolReport(t, 1), time.Now); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(spool, "00000000-0000-4000-8000-000000000000.json"), "")
	queued := false
	caller := &fakeCaller{}
	caller.during = func() {
		if !queued {
			queued = true
			if err := spoolCall(spool, spoolReport(t, 2), time.Now); err != nil {
				t.Error(err)
			}
		}
	}
	connections := 0
	sent, err := FlushSpool(context.Background(), spool, connecting(caller, &connections), time.Now)
	if err != nil || sent != 2 {
		t.Fatalf("sent %d: %v", sent, err)
	}
	if files := spooled(t, spool); len(files) != 0 {
		t.Fatalf("spool not empty: %v", files)
	}
}

func TestSpoolRetriesARejectedCallThenDropsIt(t *testing.T) {
	spool := filepath.Join(tempDir(t), "spool")
	if err := spoolCall(spool, spoolReport(t, 1), time.Now); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caller, connections := &fakeCaller{rejecting: true}, 0
	for i := range SpoolLimits.Attempts {
		now = now.Add(SpoolLimits.MaxBackoff + time.Millisecond)
		if _, err := FlushSpool(context.Background(), spool, connecting(caller, &connections), func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
		if remaining := spooled(t, spool); i < SpoolLimits.Attempts-1 && len(remaining) != 1 {
			t.Fatalf("attempt %d: %v", i+1, remaining)
		}
	}
	if files := spooled(t, spool); len(files) != 0 {
		t.Fatalf("a call rejected %d times is dropped: %v", SpoolLimits.Attempts, files)
	}
}

func TestSpoolKeepsTheNewestCallsWithinBudget(t *testing.T) {
	spool := filepath.Join(tempDir(t), "spool")
	for i := range SpoolLimits.Files + 5 {
		call := spoolReport(t, i)
		call.Set("reportId", jsjson.String(randomUUID()))
		if err := spoolCall(spool, call, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	if files := spooled(t, spool); len(files) > SpoolLimits.Files {
		t.Fatalf("%d calls kept", len(files))
	}
}

const reportCommand = "'/runtime/durin' 'hooks' 'report'"

func TestReportHandlersForClaudeCode(t *testing.T) {
	raw := `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"lint"}]}]}}`
	once, err := UpdatedReportConfig(raw, "claude", reportCommand, "")
	if err != nil {
		t.Fatal(err)
	}
	if twice, _ := UpdatedReportConfig(once, "claude", reportCommand, ""); twice != once {
		t.Fatal("a second update changes nothing")
	}
	hooks, _ := parsed(t, once).(*jsjson.Object).GetObject("hooks")
	post, _ := hooks.GetArray("PostToolUse")
	failure, _ := hooks.GetArray("PostToolUseFailure")
	want := `{"matcher":"mcp__.*","hooks":[{"type":"command","command":"` + reportCommand + `","async":true,"timeout":30}]}`
	if len(post) != 2 || jsjson.Stringify(post[1]) != want || len(failure) != 1 {
		t.Fatalf("hooks %s", jsjson.Stringify(hooks))
	}
}

func TestReportHandlersForEachFormat(t *testing.T) {
	cases := map[string]string{
		"cursor":   `{"version":1,"hooks":{"afterMCPExecution":[{"command":"` + reportCommand + `","timeout":10}]}}`,
		"windsurf": `{"hooks":{"post_mcp_tool_use":[{"command":"` + reportCommand + `","show_output":false}]}}`,
		"codex":    `{"hooks":{"PostToolUse":[{"matcher":"mcp__.*","hooks":[{"type":"command","command":"` + reportCommand + `","async":true,"timeout":30}]}]}}`,
	}
	for format, want := range cases {
		got, err := UpdatedReportConfig("{}", format, reportCommand, "")
		if err != nil {
			t.Fatal(err)
		}
		if jsjson.Stringify(parsed(t, got)) != want {
			t.Errorf("%s: %s", format, jsjson.Stringify(parsed(t, got)))
		}
	}
}

func TestReportHandlerForAPreviousPathIsReplaced(t *testing.T) {
	moved := "'/new/runtime/durin' 'hooks' 'report'"
	first, _ := UpdatedReportConfig("{}", "claude", reportCommand, "")
	updated, err := UpdatedReportConfig(first, "claude", moved, reportCommand)
	if err != nil {
		t.Fatal(err)
	}
	hooks, _ := parsed(t, updated).(*jsjson.Object).GetObject("hooks")
	post, _ := hooks.GetArray("PostToolUse")
	if len(post) != 1 || !strings.Contains(jsjson.Stringify(post[0]), moved) {
		t.Fatalf("claude %s", jsjson.Stringify(hooks))
	}
	cursor, _ := UpdatedReportConfig("{}", "cursor", reportCommand, "")
	cursor, _ = UpdatedReportConfig(cursor, "cursor", moved, reportCommand)
	hooks, _ = parsed(t, cursor).(*jsjson.Object).GetObject("hooks")
	if got := jsjson.Stringify(get(hooks, "afterMCPExecution")); got != `[{"command":"`+moved+`","timeout":10}]` {
		t.Fatalf("cursor %s", got)
	}
}

func TestReportingRefusesDisabledHooksAndUnsupportedHarnesses(t *testing.T) {
	if _, err := UpdatedReportConfig(`{"disableAllHooks":true}`, "claude", reportCommand, ""); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled hooks: %v", err)
	}
	root := tempDir(t)
	agents := AgentsFor(root, EnvMap{})
	err := InstallReporting(agents["gemini"], BridgeConfiguration{ExecutablePath: "/runtime/durin", ProfileID: reportProfile, ConfigDirectory: root})
	wantCliError(t, err, "no local tool-call reporting adapter")
	if ReportingAvailable(agents["gemini"]) {
		t.Fatal("gemini has no reporting adapter")
	}
	claude := agents["claude"]
	if !ReportingAvailable(claude) {
		t.Fatal("claude supports reporting")
	}
	writeFile(t, claude.HookConfig.Path, `{"disableAllHooks":true}`)
	if ReportingAvailable(claude) {
		t.Fatal("reporting is not offered while hooks are disabled")
	}
}

func TestInstallReportingKeepsAnUnchangedRecord(t *testing.T) {
	f := newReportingFixture(t)
	path, _ := ReportingPath(f.bridge.ConfigDirectory, "claude")
	before := readFile(t, path)
	if err := InstallReporting(f.agent, f.bridge); err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, path); after != before {
		t.Fatalf("record rewritten:\n%s\n%s", before, after)
	}
}
