package app

import (
	"net"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
)

// McpConfigLocation is the harness's user-level MCP configuration that setup wrote the Durin
// bridge into (src/local-mcp-servers.ts).
type McpConfigLocation struct {
	ConfigPath string
	Format     string // jsonc | toml
	ServerKey  string
}

// LocalServer is the local MCP server a call went to.
type LocalServer struct {
	Transport string // stdio | loopback_http
	Server    string
	Tool      string
}

// mcpScope is one table of MCP servers: a file and the key path to the table inside it.
type mcpScope struct {
	path   string
	format string
	table  []string
}

// projectFiles are the project files each harness reads, relative to a project folder.
var projectFiles = map[string][]mcpScope{
	"claude":   {{path: ".mcp.json", format: "jsonc", table: []string{"mcpServers"}}},
	"codex":    {{path: filepath.Join(".codex", "config.toml"), format: "toml", table: []string{"mcp_servers"}}},
	"cursor":   {{path: filepath.Join(".cursor", "mcp.json"), format: "jsonc", table: []string{"mcpServers"}}},
	"windsurf": nil,
}

// ancestors are the working directory and its parents, nearest first (at most 32), so a cd
// into a subfolder still finds the project.
func ancestors(cwd string) []string {
	if cwd == "" || !isAbsoluteHere(cwd) {
		return nil
	}
	var out []string
	for current := cwd; len(out) < 32; {
		out = append(out, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return out
}

// mcpScopes are the scopes in the harness's precedence order: Claude Code local (its user
// file under projects[<folder>]), then project, then user; other harnesses project, then user.
func mcpScopes(format string, agent McpConfigLocation, cwd string) []mcpScope {
	folders := ancestors(cwd)
	var out []mcpScope
	if format == "claude" {
		for _, folder := range folders {
			out = append(out, mcpScope{path: agent.ConfigPath, format: agent.Format, table: []string{"projects", folder, "mcpServers"}})
		}
	}
	for _, folder := range folders {
		for _, file := range projectFiles[format] {
			out = append(out, mcpScope{path: filepath.Join(folder, file.path), format: file.format, table: file.table})
		}
	}
	return append(out, mcpScope{path: agent.ConfigPath, format: agent.Format, table: []string{agent.ServerKey}})
}

func nested(value jsjson.Value, path []string) jsjson.Value {
	for _, key := range path {
		o, ok := value.(*jsjson.Object)
		if !ok {
			return nil
		}
		value = get(o, key)
	}
	return value
}

var unnormalizedServer = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// normalizedServer is how Claude Code rewrites a server name when it names tools.
func normalizedServer(name string) string { return unnormalizedServer.ReplaceAllString(name, "_") }

func matchingKey(table *jsjson.Object, server string) (string, bool) {
	if _, ok := table.Get(server); ok {
		return server, true
	}
	for _, key := range table.Keys() {
		if normalizedServer(key) == server {
			return key, true
		}
	}
	return "", false
}

// configReader reads each configuration file once; an unreadable or invalid file is empty.
func configReader() func(scope mcpScope) *jsjson.Object {
	files := map[string]*jsjson.Object{}
	return func(scope mcpScope) *jsjson.Object {
		if config, seen := files[scope.path]; seen {
			return config
		}
		var config *jsjson.Object
		if raw, found, err := ReadConfig(scope.path); err == nil && found {
			config, _ = ParseConfig(raw, scope.format)
		}
		files[scope.path] = config
		return config
	}
}

type mcpMatch struct {
	key, tool string
	entry     jsjson.Value
}

func foundIn(scope mcpScope, read func(mcpScope) *jsjson.Object, targets []McpTarget) *mcpMatch {
	var root jsjson.Value
	if config := read(scope); config != nil {
		root = config
	}
	table, ok := nested(root, scope.table).(*jsjson.Object)
	if !ok {
		return nil
	}
	for _, target := range targets {
		if key, ok := matchingKey(table, target.Server); ok {
			return &mcpMatch{key: key, tool: target.Tool, entry: get(table, key)}
		}
	}
	return nil
}

func loopbackHost(host string) bool {
	if host == "localhost" || host == "::1" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && !strings.Contains(host, ":") && ip.To4() != nil && strings.HasPrefix(host, "127.")
}

// loopbackURL reports whether value is a URL on localhost, 127.0.0.0/8 or ::1.
func loopbackURL(value string) bool {
	u, err := weburl.Parse(value)
	if err != nil {
		return false
	}
	return loopbackHost(strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(u.Hostname(), "["), "]")))
}

// entryArgv is [command, args].flat(), keeping strings.
func entryArgv(entry *jsjson.Object) []string {
	var out []string
	for _, key := range []string{"command", "args"} {
		switch v := get(entry, key).(type) {
		case jsjson.String:
			out = append(out, string(v))
		case jsjson.Array:
			for _, part := range v {
				if s, ok := part.(jsjson.String); ok {
					out = append(out, string(s))
				}
			}
		}
	}
	return out
}

// durinBridge reports whether entry runs the Durin bridge (<cli> mcp serve --profile ...),
// whose calls already reach the gateway.
func durinBridge(entry *jsjson.Object) bool {
	var mcp, serve, profile bool
	for _, part := range entryArgv(entry) {
		mcp = mcp || part == "mcp"
		serve = serve || part == "serve"
		profile = profile || part == "--profile" || strings.HasPrefix(part, "--profile=")
	}
	return mcp && serve && profile
}

func skippedEntry(entry *jsjson.Object) bool {
	return jsjson.Equal(get(entry, "disabled"), jsjson.Bool(true)) ||
		jsjson.Equal(get(entry, "enabled"), jsjson.Bool(false)) || durinBridge(entry)
}

// entryTransport is how a configured server runs when it is on this computer: stdio for a
// command, loopback_http for a loopback URL, and "" otherwise (remote, disabled, the Durin
// bridge, or not a server).
func entryTransport(value jsjson.Value) string {
	entry, ok := value.(*jsjson.Object)
	if !ok || skippedEntry(entry) {
		return ""
	}
	for _, key := range []string{"url", "serverUrl", "httpUrl"} {
		if url, ok := get(entry, key).(jsjson.String); ok {
			if loopbackURL(string(url)) {
				return "loopback_http"
			}
			return ""
		}
	}
	if len(entryArgv(entry)) > 0 {
		return "stdio"
	}
	return ""
}

// localServerFor finds the server a call went to, in the harness's own precedence order, and
// returns it when it runs on this computer. The first scope that defines the server decides;
// remote, disabled, unknown and Durin servers are never local.
func localServerFor(format string, agent McpConfigLocation, targets []McpTarget, cwd string) *LocalServer {
	read := configReader()
	for _, scope := range mcpScopes(format, agent, cwd) {
		match := foundIn(scope, read, targets)
		if match == nil {
			continue
		}
		transport := entryTransport(match.entry)
		if transport == "" {
			return nil
		}
		return &LocalServer{Transport: transport, Server: match.key, Tool: match.tool}
	}
	return nil
}
