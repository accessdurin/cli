package app

import (
	"os"
	"path/filepath"

	"github.com/accessdurin/cli/internal/clierror"
)

// regularText reads a regular file as UTF-8 text; found is false when it does not exist.
// Anything else at the path (a symlink, a directory) is refused.
func regularText(path string) (text string, found bool, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		if missing(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, clierror.New("Expected a regular file: " + path)
	}
	text, err = readText(path)
	if err != nil {
		if missing(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return text, true, nil
}

func checkedParents(path string) error {
	parent := filepath.Dir(path)
	if parent == path {
		return nil
	}
	if err := checkedParents(parent); err != nil {
		return err
	}
	return checkParent(parent)
}

func checkParent(parent string) error {
	trusted, err := isTrustedDirectory(parent)
	if err != nil {
		if missing(err) {
			return nil
		}
		return err
	}
	if !trusted {
		return clierror.New("Integration directories must not be symlinks: " + parent)
	}
	return nil
}

func isTrustedDirectory(path string) (bool, error) {
	entry, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if entry.IsDir() {
		return true, nil
	}
	// macOS /var and /tmp are system-owned links to /private.
	if entry.Mode()&os.ModeSymlink != 0 && fileUID(entry) == 0 {
		target, err := os.Stat(path)
		if err != nil {
			return false, err
		}
		return target.IsDir(), nil
	}
	return false, nil
}

// FileChange is a planned write of a managed file: Before is nil when it does not exist.
type FileChange struct {
	Path   string
	Before *string
	After  string
}

// PlanManagedFile plans writing after to path. An existing file is replaced only when it is
// already after or still has the digest previous recorded for what durin wrote; anything
// else (edited by the user, or not ours) is preserved.
func PlanManagedFile(path, after, previous string) (FileChange, error) {
	if err := checkedParents(path); err != nil {
		return FileChange{}, err
	}
	before, found, err := regularText(path)
	if err != nil {
		return FileChange{}, err
	}
	if !found {
		return FileChange{Path: path, After: after}, nil
	}
	if before != after && (previous == "" || textDigest(before) != previous) {
		return FileChange{}, clierror.New("Preserving an existing or edited file: " + path + ". Move it aside before retrying setup.")
	}
	return FileChange{Path: path, Before: &before, After: after}, nil
}

// saveFileChange is a variable so tests can interrupt a save, as the TypeScript tests spied
// on saveFileChange.
var saveFileChange = SaveFileChange

// SaveFileChange writes a planned change unless the file changed since it was planned,
// keeping the previous contents in a <path>.durin-backup-<uuid> file.
func SaveFileChange(change FileChange) error {
	if change.Before != nil && *change.Before == change.After {
		return nil
	}
	if err := checkedParents(change.Path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(change.Path), 0o700); err != nil {
		return err
	}
	temporary := change.Path + "." + randomUUID() + ".tmp"
	defer rmForce(temporary)
	if err := writeExclusive(temporary, []byte(change.After), 0o600); err != nil {
		return err
	}
	current, found, err := regularText(change.Path)
	if err != nil {
		return err
	}
	if found != (change.Before != nil) || (found && current != *change.Before) {
		return clierror.New("Configuration changed; retry installation: " + change.Path)
	}
	if change.Before != nil {
		if err := writeExclusive(change.Path+".durin-backup-"+randomUUID(), []byte(*change.Before), 0o600); err != nil {
			return err
		}
	}
	return os.Rename(temporary, change.Path)
}
