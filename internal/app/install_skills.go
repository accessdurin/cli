package app

import (
	"path/filepath"
	"regexp"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/zod"
)

var skillManifestSchema = zod.Object(zod.F("sha256", zod.String().Regex(regexp.MustCompile(`^[a-f0-9]{64}$`))))

func skillChanges(root string, skill bundledSkill) ([3]FileChange, error) {
	directory := filepath.Join(root, skill.Name)
	path := filepath.Join(directory, ".durin-install.json")
	raw, found, err := regularText(path)
	if err != nil {
		return [3]FileChange{}, err
	}
	previous := ""
	if found {
		value, err := jsjson.ParseString(raw)
		if err != nil {
			return [3]FileChange{}, err
		}
		manifest, err := zod.Parse(skillManifestSchema, value)
		if err != nil {
			return [3]FileChange{}, err
		}
		previous, _ = manifest.(*jsjson.Object).GetString("sha256")
	}
	content, err := PlanManagedFile(filepath.Join(directory, "SKILL.md"), skill.Content, previous)
	if err != nil {
		return [3]FileChange{}, err
	}
	license, err := PlanManagedFile(filepath.Join(directory, "LICENSE"), skillLicense, "")
	if err != nil {
		return [3]FileChange{}, err
	}
	manifest := FileChange{Path: path, After: jsjson.StringifyIndent(jsjson.Obj(
		jsjson.KV("repository", jsjson.String(skillSource.Repository)),
		jsjson.KV("revision", jsjson.String(skillSource.Revision)),
		jsjson.KV("sha256", jsjson.String(skill.SHA256)),
	), 2) + "\n"}
	if found {
		manifest.Before = &raw
	}
	return [3]FileChange{content, license, manifest}, nil
}

// InstallSkills installs the bundled skills into the agent's skills directory and returns how
// many it installed (0 when the agent has none). Every destination is validated before any
// skill is replaced; a skill the user edited is preserved.
func InstallSkills(agent contracts.AgentConfig) (int, error) {
	if agent.SkillsDirectory == "" {
		return 0, nil
	}
	var changes [][3]FileChange
	for _, skill := range bundledSkills {
		change, err := skillChanges(agent.SkillsDirectory, skill)
		if err != nil {
			return 0, err
		}
		changes = append(changes, change)
	}
	for _, change := range changes {
		for _, c := range change {
			if err := saveFileChange(c); err != nil {
				return 0, err
			}
		}
	}
	return len(bundledSkills), nil
}
