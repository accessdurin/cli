package app

import (
	"os"
	"path/filepath"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/jsonc"
	"github.com/accessdurin/cli/internal/toml"
	"github.com/accessdurin/cli/internal/zod"
)

func parseConfigValue(raw, format string) (jsjson.Value, error) {
	if format == "toml" {
		return toml.Parse(raw)
	}
	value, errs := jsonc.Parse(raw, jsonc.ParseOptions{AllowTrailingComma: true})
	if len(errs) > 0 {
		return nil, clierror.New("Invalid JSON configuration; fix it before installing.")
	}
	return fromJSONC(value), nil
}

// ParseConfig parses an agent configuration file (JSONC or TOML) into its top-level object.
func ParseConfig(raw string, format string) (*jsjson.Object, error) {
	value, err := parseConfigValue(raw, format)
	if err == nil {
		value, err = zod.Parse(recordSchema, value)
	}
	if err != nil {
		return nil, clierror.New("Invalid agent configuration. Fix its syntax before installing; no settings were changed.")
	}
	return value.(*jsjson.Object), nil
}

// ReadConfig reads an agent configuration file; found is false when it does not exist.
func ReadConfig(path string) (raw string, found bool, err error) {
	raw, err = readText(path)
	if err != nil {
		if missing(err) {
			return "", false, nil
		}
		return "", false, clierror.Wrap("Cannot read agent configuration; check its permissions.", err)
	}
	return raw, true, nil
}

// ServerFrom is the server entry named name (default "durin") under the agent's server key;
// nil when absent. A server key or entry that is not an object is an error.
func ServerFrom(config *jsjson.Object, agent contracts.AgentConfig, name string) (*jsjson.Object, error) {
	var servers jsjson.Value = jsjson.NewObject()
	if v, _ := config.Get(agent.ServerKey); v != nil {
		servers = v
	}
	parsed, err := zod.Parse(recordSchema, servers)
	if err != nil {
		return nil, err
	}
	entry, _ := parsed.(*jsjson.Object).Get(name)
	if entry == nil {
		return nil, nil
	}
	server, err := zod.Parse(recordSchema, entry)
	if err != nil {
		return nil, err
	}
	return server.(*jsjson.Object), nil
}

// EndpointFrom is the remote endpoint the agent's "durin" entry uses; "" when there is none.
func EndpointFrom(raw string, agent contracts.AgentConfig) (string, error) {
	config, err := ParseConfig(raw, agent.Format)
	if err != nil {
		return "", err
	}
	server, err := ServerFrom(config, agent, "durin")
	if err != nil || server == nil {
		return "", err
	}
	if enabled, _ := server.Get("enabled"); jsjson.Equal(enabled, jsjson.Bool(false)) {
		return "", clierror.New("Durin is disabled.")
	}
	if disabled, _ := server.Get("disabled"); jsjson.Equal(disabled, jsjson.Bool(true)) {
		return "", clierror.New("Durin is disabled.")
	}
	if err := validateTransport(server, agent); err != nil {
		return "", err
	}
	out, err := zod.Parse(EndpointSchema, get(server, agent.URLKey))
	if err != nil {
		return "", err
	}
	return string(out.(jsjson.String)), nil
}

func validateTransport(server *jsjson.Object, agent contracts.AgentConfig) error {
	if expected, ok := agent.Transport.Get("type"); ok && !jsjson.Equal(get(server, "type"), expected) {
		return clierror.New("Durin has an invalid transport type.")
	}
	if server.Has("command") && get(server, "command") != nil {
		return clierror.New("Durin must use the remote HTTP transport.")
	}
	return nil
}

func existingConfig(raw string, agent contracts.AgentConfig, endpoint string) (bool, error) {
	config, err := ParseConfig(raw, agent.Format)
	if err != nil {
		return false, err
	}
	server, err := ServerFrom(config, agent, "durin")
	if err != nil || server == nil {
		return false, err
	}
	if !jsjson.Equal(get(server, agent.URLKey), jsjson.String(endpoint)) {
		return false, clierror.New("A different Durin configuration already exists. Review it in your agent settings before replacing it.")
	}
	if _, err := EndpointFrom(raw, agent); err != nil {
		return false, err
	}
	return true, nil
}

// UpdatedConfig adds the remote "durin" server to an agent configuration, keeping everything
// else (comments included); an existing identical entry is left as it is.
func UpdatedConfig(raw string, agent contracts.AgentConfig, endpoint string) (string, error) {
	if _, err := zod.Parse(EndpointSchema, jsjson.String(endpoint)); err != nil {
		return "", err
	}
	existing, err := existingConfig(raw, agent, endpoint)
	if err != nil || existing {
		return raw, err
	}
	server := agent.Transport.Clone().Set(agent.URLKey, jsjson.String(endpoint))
	var updated string
	if agent.Format == "toml" {
		updated = raw + "\n[mcp_servers.durin]\nurl = " + jsjson.Quote(endpoint) + "\n"
	} else if updated, err = jsoncEdit(raw, []any{agent.ServerKey, "durin"}, server); err != nil {
		return "", err
	}
	if _, err := EndpointFrom(updated, agent); err != nil {
		return "", err
	}
	return updated, nil
}

func emptyConfig(format string) string {
	if format == "toml" {
		return ""
	}
	return "{}"
}

func configFilePath(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if missing(err) {
			return path, nil
		}
		return "", err
	}
	return real, nil
}

// WriteAgentConfig rewrites the agent's configuration file through update, following a
// symlinked file to its target, unless the file changed meanwhile.
func WriteAgentConfig(agent contracts.AgentConfig, update func(raw string) (string, error)) error {
	path, err := configFilePath(agent.ConfigPath)
	if err != nil {
		return err
	}
	raw, found, err := ReadConfig(path)
	if err != nil {
		return err
	}
	initial := emptyConfig(agent.Format)
	if found {
		initial = raw
	}
	updated, err := update(initial)
	if err != nil {
		return err
	}
	if found && updated == raw {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	temporary := path + "." + randomUUID() + ".tmp"
	defer rmForce(temporary)
	if err := writeExclusive(temporary, []byte(updated), 0o600); err != nil {
		return err
	}
	current, stillFound, err := ReadConfig(path)
	if err != nil {
		return err
	}
	if stillFound != found || current != raw {
		return clierror.New("Configuration changed; retry installation.")
	}
	return os.Rename(temporary, path)
}

// WriteConfig adds the remote "durin" server to the agent's configuration file.
func WriteConfig(agent contracts.AgentConfig, endpoint string) error {
	return WriteAgentConfig(agent, func(raw string) (string, error) { return UpdatedConfig(raw, agent, endpoint) })
}
