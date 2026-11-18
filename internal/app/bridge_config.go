package app

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/toml"
	"github.com/accessdurin/cli/internal/zod"
)

// BridgeConfiguration is what an agent needs to start the stdio bridge for a profile:
// <ExecutablePath> mcp serve --profile <ProfileID> --config-dir <ConfigDirectory>.
//
// The TypeScript CLI ran a separate Node executable on an installed cli.mjs (nodePath and
// cliPath); the Go CLI runs its own installed executable (InstallRuntime's result).
type BridgeConfiguration struct {
	ExecutablePath  string
	ConfigDirectory string
	ProfileID       string
}

var bridgeSchema = zod.Object(
	zod.F("executablePath", absolutePath()),
	zod.F("configDirectory", absolutePath()),
	zod.F("profileId", contracts.ProfileIDSchema),
).Strict()

func (b BridgeConfiguration) validate() error {
	_, err := zod.Parse(bridgeSchema, jsjson.Obj(
		jsjson.KV("executablePath", jsjson.String(b.ExecutablePath)),
		jsjson.KV("configDirectory", jsjson.String(b.ConfigDirectory)),
		jsjson.KV("profileId", jsjson.String(b.ProfileID)),
	))
	return err
}

// BridgeServerName is the agent's server name for a profile: durin-<first 12 of its id>.
func BridgeServerName(profileID string) (string, error) {
	if !contracts.ValidProfileID(profileID) {
		_, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(profileID))
		return "", err
	}
	return "durin-" + profileID[:12], nil
}

func serveArguments(input BridgeConfiguration) []string {
	return []string{"mcp", "serve", "--profile", input.ProfileID, "--config-dir", input.ConfigDirectory}
}

// bridgeEntry is {...agent.bridgeTransport, command, args} (or command as one array for
// command-array harnesses) for the given executable and leading arguments.
func bridgeEntry(agent contracts.AgentConfig, executable string, args []string) *jsjson.Object {
	entry := agent.BridgeTransport.Clone()
	if agent.Bridge == "command-array" {
		return entry.Set("command", jsjson.Strings(append([]string{executable}, args...)...))
	}
	return entry.Set("command", jsjson.String(executable)).Set("args", jsjson.Strings(args...))
}

func sameEntry(existing, expected *jsjson.Object) bool {
	if jsjson.Equal(get(existing, "disabled"), jsjson.Bool(true)) || jsjson.Equal(get(existing, "enabled"), jsjson.Bool(false)) ||
		get(existing, "url") != nil || get(existing, "serverUrl") != nil {
		return false
	}
	for _, m := range expected.Members() {
		if jsjson.Stringify(get(existing, m.Key)) != jsjson.Stringify(m.Value) {
			return false
		}
	}
	return true
}

// legacyRuntime is the launcher the TypeScript CLI installed and pointed agents at.
func legacyRuntime(configDirectory string) string {
	return filepath.Join(configDirectory, "runtime", "cli.mjs")
}

// legacyEntry is the entry the TypeScript CLI wrote for this bridge, with the Node path the
// existing entry names; nil when the existing entry cannot be one.
func legacyEntry(existing *jsjson.Object, agent contracts.AgentConfig, input BridgeConfiguration) *jsjson.Object {
	command := get(existing, "command")
	if agent.Bridge == "command-array" {
		if parts, ok := command.(jsjson.Array); ok && len(parts) > 0 {
			command = parts[0]
		}
	}
	node, ok := command.(jsjson.String)
	if !ok || !isAbsoluteHere(string(node)) || strings.ContainsAny(string(node), "\r\n\x00") {
		return nil
	}
	return bridgeEntry(agent, string(node), append([]string{legacyRuntime(input.ConfigDirectory)}, serveArguments(input)...))
}

// UpdatedBridgeConfig adds the stdio bridge entry for input's profile to an agent
// configuration. An identical entry is kept; an entry the TypeScript CLI wrote for the same
// profile and directory is replaced (comments outside it are kept); any other entry under the
// same name is a conflict.
func UpdatedBridgeConfig(raw string, agent contracts.AgentConfig, input BridgeConfiguration) (string, error) {
	if err := input.validate(); err != nil {
		return "", err
	}
	name, err := BridgeServerName(input.ProfileID)
	if err != nil {
		return "", err
	}
	entry := bridgeEntry(agent, input.ExecutablePath, serveArguments(input))
	config, err := ParseConfig(raw, agent.Format)
	if err != nil {
		return "", err
	}
	existing, err := ServerFrom(config, agent, name)
	if err != nil {
		return "", err
	}
	conflict := clierror.New("This organization already has a conflicting agent configuration. Review it before running durin again.")
	var updated string
	switch {
	case existing != nil && sameEntry(existing, entry):
		return raw, nil
	case existing != nil:
		legacy := legacyEntry(existing, agent, input)
		if legacy == nil || !sameEntry(existing, legacy) {
			return "", conflict
		}
		if agent.Format == "toml" {
			if updated, err = replaceTomlTable(raw, config, agent, name, entry); err != nil {
				return "", conflict
			}
		} else if updated, err = jsoncEdit(raw, []any{agent.ServerKey, name}, entry); err != nil {
			return "", err
		}
	case agent.Format == "toml":
		table, err := toml.Stringify(jsjson.Obj(jsjson.KV(agent.ServerKey, jsjson.Obj(jsjson.KV(name, entry)))))
		if err != nil {
			return "", err
		}
		updated = raw + "\n" + table
	default:
		if updated, err = jsoncEdit(raw, []any{agent.ServerKey, name}, entry); err != nil {
			return "", err
		}
	}
	if _, err := ParseConfig(updated, agent.Format); err != nil {
		return "", err
	}
	return updated, nil
}

var tableHeader = regexp.MustCompile(`^\s*\[`)

// replaceTomlTable rewrites only the [<serverKey>.<name>] table of a TOML document with
// entry. The result must parse to the same document with only that table changed; otherwise
// (an inline table, dotted keys, an unusual layout) it fails and nothing is replaced.
func replaceTomlTable(raw string, config *jsjson.Object, agent contracts.AgentConfig, name string, entry *jsjson.Object) (string, error) {
	header := regexp.MustCompile(`^\s*\[\s*` + regexp.QuoteMeta(agent.ServerKey) + `\s*\.\s*(?:"` + regexp.QuoteMeta(name) + `"|'` +
		regexp.QuoteMeta(name) + `'|` + regexp.QuoteMeta(name) + `)\s*\]\s*(?:#.*)?$`)
	lines := strings.SplitAfter(raw, "\n")
	start := -1
	for i, line := range lines {
		if header.MatchString(strings.TrimRight(line, "\r\n")) {
			if start >= 0 {
				return "", errTomlLayout
			}
			start = i
		}
	}
	if start < 0 {
		return "", errTomlLayout
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if tableHeader.MatchString(lines[i]) {
			end = i
			break
		}
	}
	// Blank lines and comments that end the table belong to whatever follows it.
	for end > start+1 {
		trimmed := strings.TrimSpace(lines[end-1])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		end--
	}
	table, err := toml.Stringify(jsjson.Obj(jsjson.KV(agent.ServerKey, jsjson.Obj(jsjson.KV(name, entry)))))
	if err != nil {
		return "", err
	}
	if end < len(lines) && !strings.HasSuffix(lines[end-1], "\n") {
		return "", errTomlLayout
	}
	updated := strings.Join(lines[:start], "") + table + strings.Join(lines[end:], "")
	parsed, err := ParseConfig(updated, agent.Format)
	if err != nil {
		return "", err
	}
	saved, err := ServerFrom(parsed, agent, name)
	if err != nil || saved == nil || jsjson.Stringify(saved) != jsjson.Stringify(entry) {
		return "", errTomlLayout
	}
	if withoutServer(parsed, agent.ServerKey, name) != withoutServer(config, agent.ServerKey, name) {
		return "", errTomlLayout
	}
	return updated, nil
}

var errTomlLayout = clierror.New("unsupported TOML layout for the bridge entry")

func withoutServer(config *jsjson.Object, serverKey, name string) string {
	copy := config.Clone()
	if servers, ok := copy.GetObject(serverKey); ok {
		servers = servers.Clone()
		servers.Delete(name)
		copy.Set(serverKey, servers)
	}
	return jsjson.Stringify(copy)
}

// WriteBridgeConfig adds the bridge entry to the agent's configuration file.
func WriteBridgeConfig(agent contracts.AgentConfig, input BridgeConfiguration) error {
	return WriteAgentConfig(agent, func(raw string) (string, error) { return UpdatedBridgeConfig(raw, agent, input) })
}
