package app

import (
	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/jsjson"
)

// ProfileCommand is `durin profiles list|use <id>` (src/profile-commands.ts).
func ProfileCommand(options *CliOptions, io *IO) error {
	profiles := NewFileProfiles(options.Directory)
	action := positional(options, 1)
	if action == "list" {
		list, err := profiles.List()
		if err != nil {
			return err
		}
		return PrintJSON(io.Stdout, ProfilesJSON(list))
	}
	if action != "use" {
		return clierror.New("Use durin profiles list or durin profiles use <id>.")
	}
	id := positional(options, 2)
	if id == "" {
		return clierror.New("Provide a profile ID from durin profiles list.")
	}
	if err := profiles.Activate(id); err != nil {
		return err
	}
	return PrintJSON(io.Stdout, jsjson.Obj(jsjson.KV("activeProfile", jsjson.String(id))))
}

// positional is options.positionals[i], "" when absent.
func positional(options *CliOptions, i int) string {
	if i < len(options.Positionals) {
		return options.Positionals[i]
	}
	return ""
}
