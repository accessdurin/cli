package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// CreateProfile is createProfile (src/profiles.ts).
func CreateProfile(scope contracts.CliProfileScope) (contracts.CliProfile, error) {
	return contracts.CreateProfile(scope)
}

func invalidProfile(issues *zod.Error) string {
	for _, issue := range issues.Issues {
		if issue.Code != "custom" || len(issue.Path) == 0 {
			continue
		}
		for _, role := range contracts.CliProfileHostRoles {
			if issue.Path[0] == string(role) {
				return issue.Message + " Run durin to sign in again."
			}
		}
	}
	return "Invalid Durin profile binding."
}

func parseProfile(value jsjson.Value) (contracts.CliProfile, error) {
	parsed, issues, err := contracts.SafeParseCliProfile(value)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	if issues != nil {
		return contracts.CliProfile{}, clierror.New(invalidProfile(issues))
	}
	expected, err := contracts.CreateProfile(parsed.CliProfileScope)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	if parsed.ID != expected.ID {
		return contracts.CliProfile{}, clierror.New("Invalid Durin profile binding. Run durin to configure it again.")
	}
	return expected, nil
}

// FileProfiles stores profiles as <directory>/profiles/<id>.json and the active profile's id
// in <directory>/active (ports.CliProfiles).
type FileProfiles struct{ Directory string }

var _ ports.CliProfiles = FileProfiles{}

// NewFileProfiles returns the profiles kept in directory.
func NewFileProfiles(directory string) FileProfiles { return FileProfiles{Directory: directory} }

// Save writes a validated profile.
func (p FileProfiles) Save(profile contracts.CliProfile) error {
	parsed, err := parseProfile(profile.JSON())
	if err != nil {
		return err
	}
	path, err := p.path(parsed.ID)
	if err != nil {
		return err
	}
	return AtomicWrite(path, jsjson.StringifyIndent(parsed.JSON(), 2)+"\n")
}

// Load reads a profile; "" loads the active one.
func (p FileProfiles) Load(id string) (contracts.CliProfile, error) {
	selected := id
	if id == "" {
		raw, err := readText(filepath.Join(p.Directory, "active"))
		if err != nil {
			return contracts.CliProfile{}, err
		}
		selected = strings.TrimFunc(raw, zod.IsJSWhiteSpace)
	}
	return p.load(selected)
}

func (p FileProfiles) load(selected string) (contracts.CliProfile, error) {
	path, err := p.path(selected)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	value, err := jsjson.Parse(data)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	profile, err := parseProfile(value)
	if err != nil {
		return contracts.CliProfile{}, err
	}
	if profile.ID != selected {
		return contracts.CliProfile{}, clierror.New("Invalid Durin profile binding.")
	}
	return profile, nil
}

var profileFileName = regexp.MustCompile(`^[a-f0-9]{64}\.json$`)

func refusalReason(err error) string {
	if expected, ok := clierror.As(err); ok {
		return expected.Message
	}
	return "Invalid Durin profile binding."
}

// List loads every saved profile, in directory order. A profile that no longer loads is
// listed as refused, with its reason, rather than failing the whole list.
func (p FileProfiles) List() (ports.ProfileList, error) {
	entries, err := os.ReadDir(filepath.Join(p.Directory, "profiles"))
	if err != nil && !missing(err) {
		return ports.ProfileList{}, err
	}
	list := ports.ProfileList{Profiles: []contracts.CliProfile{}, Refused: []ports.RefusedProfile{}}
	for _, entry := range entries {
		if !profileFileName.MatchString(entry.Name()) {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		profile, err := p.load(id)
		if err != nil {
			list.Refused = append(list.Refused, ports.RefusedProfile{ID: id, Reason: refusalReason(err)})
			continue
		}
		list.Profiles = append(list.Profiles, profile)
	}
	return list, nil
}

// Activate makes id the active profile.
func (p FileProfiles) Activate(id string) error {
	profile, err := p.load(id)
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(p.Directory, "active"), profile.ID+"\n")
}

func (p FileProfiles) path(id string) (string, error) {
	if _, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(id)); err != nil {
		return "", err
	}
	return filepath.Join(p.Directory, "profiles", id+".json"), nil
}

// ProfilesJSON is a list of profiles as printed. Accepted profiles keep their saved shape; a
// refused one is in the same array as {id, refused: true, reason}.
func ProfilesJSON(list ports.ProfileList) jsjson.Array {
	out := jsjson.Array{}
	for _, profile := range list.Profiles {
		out = append(out, profile.JSON())
	}
	for _, refused := range list.Refused {
		out = append(out, jsjson.Obj(
			jsjson.KV("id", jsjson.String(refused.ID)),
			jsjson.KV("refused", jsjson.Bool(true)),
			jsjson.KV("reason", jsjson.String(refused.Reason)),
		))
	}
	return out
}

// AtomicWrite writes a private file so that readers see either the old or the new contents,
// never a mix.
func AtomicWrite(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + "." + randomUUID() + ".tmp"
	defer rmForce(temporary)
	if err := writeExclusive(temporary, []byte(value), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
