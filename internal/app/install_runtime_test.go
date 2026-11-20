package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/accessdurin/cli/internal/version"
)

func runtimeVersions(t *testing.T, config string) []string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(config, "runtime"))
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, filepath.Join(config, "runtime", e.Name()))
		}
	}
	return versions
}

func TestInstallRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions and shell scripts")
	}
	dir := tempDir(t)
	source := filepath.Join(dir, "release", "bin", "durin")
	writeFile(t, source, "#!/bin/sh\necho runtime-v1\n")
	writeFile(t, filepath.Join(dir, "release", "THIRD_PARTY_LICENSES.md"), "notices")
	config := filepath.Join(dir, "config")

	installed, err := InstallRuntime(config, source)
	if err != nil || installed != filepath.Join(config, "runtime", "durin") {
		t.Fatalf("%s %v", installed, err)
	}
	if again, err := InstallRuntime(config, source); err != nil || again != installed {
		t.Fatalf("reuse: %v", err)
	}
	versions := runtimeVersions(t, config)
	if len(versions) != 1 || readFile(t, filepath.Join(versions[0], "LICENSE")) != version.License || readFile(t, filepath.Join(versions[0], "THIRD_PARTY_LICENSES.md")) != "notices" {
		t.Fatalf("versions %v", versions)
	}
	_ = os.RemoveAll(filepath.Join(dir, "release"))
	if out, err := exec.Command(installed).Output(); err != nil || string(out) != "runtime-v1\n" {
		t.Fatalf("published copy: %q %v", out, err)
	}

	// A different build installs beside the existing one and replaces the published copy.
	writeFile(t, source, "#!/bin/sh\necho runtime-v2\n")
	if _, err := InstallRuntime(config, source); err != nil {
		t.Fatal(err)
	}
	if len(runtimeVersions(t, config)) != 2 {
		t.Fatal("want two versions")
	}
	if out, _ := exec.Command(installed).Output(); string(out) != "runtime-v2\n" {
		t.Fatalf("published copy not replaced: %q", out)
	}

	// Reuse demands an intact version that only this user can modify.
	tamper := []func(version string){
		func(v string) { writeFile(t, filepath.Join(v, "durin"), "planted") },
		func(v string) { writeFile(t, filepath.Join(v, "planted.js"), "planted") },
		func(v string) { _ = os.Chmod(v, 0o777) },
	}
	for _, change := range tamper {
		_ = os.RemoveAll(filepath.Join(config, "runtime"))
		if _, err := InstallRuntime(config, source); err != nil {
			t.Fatal(err)
		}
		change(runtimeVersions(t, config)[0])
		_, err := InstallRuntime(config, source)
		wantCliError(t, err, "does not match this CLI")
	}

	_ = os.Chmod(filepath.Join(config, "runtime"), 0o777)
	_, err = InstallRuntime(config, source)
	wantCliError(t, err, filepath.Join(config, "runtime")+" must be a directory owned by you")
	open := filepath.Join(dir, "open")
	_ = os.Mkdir(open, 0o777)
	_ = os.Chmod(open, 0o777)
	_, err = InstallRuntime(open, source)
	wantCliError(t, err, "chmod 700")
}
