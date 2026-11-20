package app

import (
	"os"
	"regexp"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

var customDescriptorSchema = zod.Object(
	zod.F("id", zod.String().Regex(regexp.MustCompile(`^custom(?:-[a-z0-9-]+)?$`))),
	zod.F("name", zod.String().Min(1).Max(80)),
	zod.F("configPath", absolutePath()),
	zod.F("format", zod.Enum("jsonc", "toml")),
	zod.F("serverKey", zod.String().Regex(regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`))),
	zod.F("bridge", zod.Enum("arguments", "command-array")),
	zod.F("bridgeTransport", zod.Default(
		zod.Record(zod.Union(zod.String(), zod.Boolean(), zod.Array(zod.String()))),
		func() jsjson.Value { return jsjson.NewObject() })),
	zod.F("skillsDirectory", absolutePath()),
	zod.F("hookConfig", zod.Optional(zod.Object(
		zod.F("path", absolutePath()),
		zod.F("format", zod.Enum(HookFormats...)),
	))),
).Strict()

// ReadCustomHarness reads a custom harness descriptor (docs/agent-setup.md).
func ReadCustomHarness(file string) (contracts.AgentConfig, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return contracts.AgentConfig{}, err
	}
	value, err := jsjson.Parse(data)
	if err != nil {
		return contracts.AgentConfig{}, err
	}
	out, zerr := zod.SafeParse(customDescriptorSchema, value)
	if zerr != nil {
		return contracts.AgentConfig{}, clierror.New("Invalid custom harness descriptor. See docs/agent-setup.md.")
	}
	o := out.(*jsjson.Object)
	agent := contracts.AgentConfig{URLKey: "url", Transport: jsjson.NewObject(), Authentication: ""}
	agent.ID, _ = o.GetString("id")
	agent.Name, _ = o.GetString("name")
	agent.ConfigPath, _ = o.GetString("configPath")
	agent.Format, _ = o.GetString("format")
	agent.ServerKey, _ = o.GetString("serverKey")
	agent.Bridge, _ = o.GetString("bridge")
	agent.BridgeTransport, _ = o.GetObject("bridgeTransport")
	agent.SkillsDirectory, _ = o.GetString("skillsDirectory")
	if hook, ok := o.GetObject("hookConfig"); ok {
		path, _ := hook.GetString("path")
		format, _ := hook.GetString("format")
		agent.HookConfig = &contracts.HookConfig{Path: path, Format: contracts.HookFormat(format)}
	}
	return agent, nil
}
