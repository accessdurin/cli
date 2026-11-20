package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// Help, argument validation and option errors: the command-line surface.
func TestArguments(t *testing.T) {
	e := NewEnv(t)
	cases := [][]string{
		{"--help"},
		{"-h"},
		{"tools", "call", "--help"},
		{"bogus", "--config-dir", e.Config},
		{"profiles", "--config-dir", e.Config},
		{"profiles", "delete", "--config-dir", e.Config},
		{"verify", "extra", "--config-dir", e.Config},
		{"verify", "--force", "--config-dir", e.Config},
		{"verify", "--nope", "--config-dir", e.Config},
		{"verify", "--profile"},
		{"login", "--profile", "x", "--config-dir", e.Config},
		{"tools", "--config-dir", e.Config},
		{"tools", "list", "x", "--config-dir", e.Config},
		{"mcp", "--config-dir", e.Config},
		{"hooks", "--config-dir", e.Config},
		{"hooks", "run", "--config-dir", e.Config},
		{"--app-url", "http://example.com", "--config-dir", e.Config},
		{"--app-url", "https://example.com/path", "--config-dir", e.Config},
		{"--app-url", "https://user@example.com", "--config-dir", e.Config},
		{"--app-url", "https://example.com?x=1", "--config-dir", e.Config},
		{"--app-url", "https://example.com..", "--config-dir", e.Config},
		{"--app-url", "http://127.0.0.1:9", "--config-dir", e.Config},
		{"--app-url", "not a url", "--config-dir", e.Config},
		{"--app-url", "https://EXAMPLE.com", "--config-dir", e.Config},
		{"--app-url", "https://example.com.", "--config-dir", e.Config},
		{"--dev", "--app-url", "http://127.0.0.1:9", "--config-dir", e.Config},
		{"--dev", "--app-url", "http://example.com", "--config-dir", e.Config},
		{"install", "--config-dir", e.Config},
		{"signup", "--config-dir", e.Config},
		{"--config-dir", e.Config},
		{"--config-dir=" + e.Config, "profiles", "list"},
		{"profiles", "list", "--config-dir", e.Config, "--config-dir", filepath.Join(e.Root, "other")},
		{"--", "profiles", "list", "--config-dir", e.Config},
	}
	for _, args := range cases {
		e.Run("", args...)
	}
}

// The configuration directory must be private: agents execute the runtime stored there.
func TestConfigDirectoryPrivacy(t *testing.T) {
	e := NewEnv(t)
	open := filepath.Join(e.Root, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	e.Run("", "profiles", "list", "--config-dir", open)

	file := filepath.Join(e.Root, "file")
	e.WriteFile(file, "x", 0o600)
	e.Run("", "profiles", "list", "--config-dir", file)

	shared := filepath.Join(e.Root, "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(shared, 0o777)
	nested := filepath.Join(shared, "durin")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	e.Run("", "profiles", "list", "--config-dir", nested)

	sticky := filepath.Join(e.Root, "sticky")
	if err := os.Mkdir(sticky, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(sticky, 0o777|os.ModeSticky)
	inside := filepath.Join(sticky, "durin")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	e.Run("", "profiles", "list", "--config-dir", inside)

	e.Run("", "profiles", "list", "--config-dir", filepath.Join(e.Root, "missing"))
	e.Note("missing directory created?", existence(filepath.Join(e.Root, "missing")))

	link := filepath.Join(e.Root, "link")
	if err := os.Symlink(e.Config, link); err != nil {
		t.Fatal(err)
	}
	e.Run("", "profiles", "list", "--config-dir", link)

	// The default directory is ~/.config/durin.
	e.Run("", "profiles", "list")
	e.Note("default directory created?", existence(filepath.Join(e.Home, ".config", "durin")))

	// Without a TTY, onboarding refuses after the directory check.
	e.Run("", "--config-dir", e.Config)
	e.Run("", "login", "--config-dir", open)
}

func existence(path string) string {
	if _, err := os.Lstat(path); err == nil {
		return "exists\n"
	}
	return "absent\n"
}

func TestProfiles(t *testing.T) {
	e := NewEnv(t)
	e.Run("", "profiles", "list", "--config-dir", e.Config)
	e.Note("files after empty list", e.Tree(e.Config))

	first := e.SaveProfile(e.DefaultProfile())
	second := e.DefaultProfile()
	second.PrincipalID = "user_02"
	secondID := e.SaveProfile(second)
	e.RunSortedList("principalId", "profiles", "list", "--config-dir", e.Config)
	e.Run("", "profiles", "use", secondID, "--config-dir", e.Config)
	e.Note("active", ReadFile(filepath.Join(e.Config, "active")))
	e.Note("files after use", e.Tree(e.Config))
	e.Run("", "profiles", "use", first, "--config-dir", e.Config)
	e.Note("active", ReadFile(filepath.Join(e.Config, "active")))
	e.Run("", "profiles", "use", "not-an-id", "--config-dir", e.Config)
	e.Run("", "profiles", "use", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--config-dir", e.Config)
	e.Run("", "profiles", "use", "--config-dir", e.Config)

	// Unrelated files in the profiles directory are ignored.
	e.WriteFile(filepath.Join(e.Config, "profiles", "notes.txt"), "x", 0o600)
	e.RunSortedList("principalId", "profiles", "list", "--config-dir", e.Config)

	// A tampered binding (id no longer matches its scope) is refused everywhere; the list names
	// it as refused, without failing, and logout deletes it and its credentials locally. It is
	// not a production profile, so logout makes no request.
	tampered := e.DefaultProfile()
	tampered.PrincipalID = "user_03"
	tamperedID := e.SaveProfile(tampered)
	path := filepath.Join(e.Config, "profiles", tamperedID+".json")
	data, _ := os.ReadFile(path)
	e.WriteFile(path, string(replaceOnce(data, "user_03", "user_04")), 0o600)
	e.RunSortedList("principalId", "profiles", "list", "--config-dir", e.Config)
	e.Run("", "profiles", "use", tamperedID, "--config-dir", e.Config)
	e.SeedGrant(tamperedID, "access-tampered", "refresh-tampered")
	e.Run("", "logout", "--profile", tamperedID, "--config-dir", e.Config)
	e.NoteRequests()
	e.Note("credentials", e.CredentialKeys("mcp:"+tamperedID))
	e.Note("files after refused logout", e.Tree(e.Config))

	// A production profile on a loopback HTTP host is not a valid binding.
	insecure := e.DefaultProfile()
	insecure.PrincipalID = "user_05"
	insecure.Development = false
	insecureID := e.SaveProfile(insecure)
	e.Run("", "profiles", "use", insecureID, "--config-dir", e.Config)

	// A known deployment's profile must name that deployment's exact hosts.
	known := Profile{
		AppOrigin: "https://app.getdurin.com", AccountIssuer: "https://auth.getdurin.com", AccountClientID: "client",
		PrincipalID: "user_06", WorkosOrganizationID: "org_wos", OrganizationID: "org", Environment: "production",
		Resource: "https://mcp.getdurin.com/mcp/u/" + resourceHex, McpIssuer: "https://auth.getdurin.com", Development: false,
	}
	knownID := e.SaveProfile(known)
	e.Run("", "profiles", "use", knownID, "--config-dir", e.Config)
	wrong := known
	wrong.PrincipalID = "user_07"
	wrong.McpIssuer = "https://evil.example"
	wrongID := e.SaveProfile(wrong)
	e.Run("", "profiles", "use", wrongID, "--config-dir", e.Config)
	slash := known
	slash.PrincipalID = "user_08"
	slash.McpIssuer = "https://auth.getdurin.com/"
	slashID := e.SaveProfile(slash)
	e.Run("", "profiles", "use", slashID, "--config-dir", e.Config)
	e.Note("active", ReadFile(filepath.Join(e.Config, "active")))

	// An explicit --profile that does not exist, and a broken active file.
	e.Run("", "mcp", "endpoint", "--profile", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "--config-dir", e.Config)
	e.WriteFile(filepath.Join(e.Config, "active"), "garbage\n", 0o600)
	e.Run("", "mcp", "endpoint", "--config-dir", e.Config)
}

func replaceOnce(data []byte, old, replacement string) []byte {
	s := string(data)
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return []byte(s[:i] + replacement + s[i+len(old):])
		}
	}
	return data
}
