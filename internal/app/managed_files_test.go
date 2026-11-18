package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagedFilesPreserveUserChanges(t *testing.T) {
	dir := tempDir(t)
	path := filepath.Join(dir, "skills", "x", "SKILL.md")
	change, err := PlanManagedFile(path, "v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveFileChange(change); err != nil {
		t.Fatal(err)
	}
	// Ours (by digest) is replaced and backed up; an edited file is preserved.
	change, err = PlanManagedFile(path, "v2", textDigest("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveFileChange(change); err != nil || readFile(t, path) != "v2" {
		t.Fatalf("replace: %v", err)
	}
	backups, _ := filepath.Glob(path + ".durin-backup-*")
	if len(backups) != 1 || readFile(t, backups[0]) != "v1" {
		t.Fatalf("backups: %v", backups)
	}
	writeFile(t, path, "user-edited")
	_, err = PlanManagedFile(path, "v3", textDigest("v2"))
	wantCliError(t, err, "Preserving")

	// A file changed after planning is not replaced.
	changed := filepath.Join(dir, "changed.txt")
	plan, _ := PlanManagedFile(changed, "planned", "")
	writeFile(t, changed, "concurrent edit")
	wantCliError(t, SaveFileChange(plan), "Configuration changed")
	if readFile(t, changed) != "concurrent edit" {
		t.Fatal("concurrent edit lost")
	}
	if runtime.GOOS == "windows" {
		return
	}
	// Symlinks: never a destination, never a parent (unless root-owned, like macOS /tmp).
	writeFile(t, filepath.Join(dir, "original.txt"), "original")
	_ = os.Symlink(filepath.Join(dir, "original.txt"), filepath.Join(dir, "link.txt"))
	_, err = PlanManagedFile(filepath.Join(dir, "link.txt"), "replacement", "")
	wantCliError(t, err, "regular file")
	linked := filepath.Join(dir, "linked")
	_ = os.Symlink(filepath.Join(dir, "skills"), linked)
	if os.Getuid() == 0 {
		_ = os.Lchown(linked, 4242, 4242) // root-owned links are trusted
	}
	_, err = PlanManagedFile(filepath.Join(linked, "x", "SKILL.md"), "v", "")
	wantCliError(t, err, "must not be symlinks")
}
