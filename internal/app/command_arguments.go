package app

import (
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
)

type argumentRule struct {
	positions int
	flags     []string
}

var (
	profileFlags    = []string{"profile", "config-dir"}
	onboardingFlags = []string{"config-dir", "app-url", "dev"}
)

var argumentRules = map[string]argumentRule{
	"":                 {0, onboardingFlags},
	"login":            {1, onboardingFlags},
	"signup":           {1, onboardingFlags},
	"install":          {1, onboardingFlags},
	"verify":           {1, profileFlags},
	"logout":           {1, append(append([]string(nil), profileFlags...), "force")},
	"tools list":       {2, profileFlags},
	"tools inspect":    {3, profileFlags},
	"tools call":       {3, append(append([]string(nil), profileFlags...), "input", "idempotency-key", "approval-id")},
	"mcp serve":        {2, profileFlags},
	"mcp endpoint":     {2, profileFlags},
	"profiles list":    {2, []string{"config-dir"}},
	"profiles use":     {3, []string{"config-dir"}},
	"hooks status":     {2, []string{"config-dir"}},
	"hooks run":        {2, []string{"config-dir", "harness", "event"}},
	"hooks report":     {2, []string{"config-dir", "harness"}},
	"hooks flush":      {2, profileFlags},
	"observe":          {1, []string{"config-dir", "supervise"}},
	"observer status":  {2, []string{"config-dir"}},
	"observer install": {2, []string{"config-dir"}},
	"observer remove":  {2, []string{"config-dir"}},
	"observer purge":   {2, []string{"config-dir"}},
	"observer export":  {2, []string{"config-dir"}},
}

func commandKey(positions []string) string {
	first := ""
	if len(positions) > 0 {
		first = positions[0]
	}
	switch first {
	case "tools", "mcp", "profiles", "hooks", "observer":
		return strings.Join(positions[:min(2, len(positions))], " ")
	}
	return first
}

// ValidateCommandArguments is validateCommandArguments (src/command-arguments.ts): the
// command must exist, with exactly its positionals and only its flags.
func ValidateCommandArguments(options *CliOptions) error {
	rule, ok := argumentRules[commandKey(options.Positionals)]
	if !ok {
		return clierror.New("Unknown command. Run durin --help.")
	}
	valid := len(options.Positionals) == rule.positions
	for _, flag := range options.Values.Keys {
		if !containsString(rule.flags, flag) {
			valid = false
		}
	}
	if !valid {
		return clierror.New("Invalid arguments for this command. Run durin --help.")
	}
	return nil
}

func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
