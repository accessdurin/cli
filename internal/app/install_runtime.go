package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/version"
)

// RuntimeExecutableName is the installed executable's file name.
func RuntimeExecutableName() string {
	if runtime.GOOS == "windows" {
		return "durin.exe"
	}
	return "durin"
}

// CurrentExecutable is the running executable's path with symlinks resolved, the source
// InstallRuntime copies.
func CurrentExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// InstallRuntime copies the executable at source into <directory>/runtime/<id>/, where id is
// the SHA-256 of the staged tree's digest, beside the CLI's LICENSE (and its third-party
// notices when they are installed next to source), then publishes a stable copy at
// <directory>/runtime/durin[.exe] and returns that path. Agents run the stable copy, so a
// package manager may replace or remove the CLI without breaking them. An existing version
// directory is reused only if it is intact and only this user can modify it.
//
// This replaces the TypeScript CLI's runtime (cli.js, its native packages and a cli.mjs
// launcher run by Node) with the single Go executable.
func InstallRuntime(directory, source string) (string, error) {
	code, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	runtimeDir := filepath.Join(directory, "runtime")
	temporary := filepath.Join(runtimeDir, ".install-"+randomUUID())
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	if err := AssertPrivateDirectory(directory); err != nil {
		return "", err
	}
	if err := AssertPrivateDirectory(runtimeDir); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(temporary, RuntimeExecutableName()), code, 0o700); err != nil {
		return "", err
	}
	if err := copyRuntimeLicenses(source, temporary); err != nil {
		return "", err
	}
	if err := restrictTree(temporary); err != nil {
		return "", err
	}
	digest, err := treeDigest(temporary)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(digest))
	id := hex.EncodeToString(sum[:])
	versionDir := filepath.Join(runtimeDir, id)
	if err := os.Rename(temporary, versionDir); err != nil {
		if !existingVersion(err, versionDir) {
			return "", err
		}
		if err := assertSameRuntime(digest, versionDir); err != nil {
			return "", err
		}
	}
	return publishExecutable(runtimeDir, code)
}

// copyRuntimeLicenses writes the CLI's LICENSE and, when a release installed them beside
// source (or one directory up, as the npm package does), its third-party notices.
func copyRuntimeLicenses(source, target string) error {
	if err := os.WriteFile(filepath.Join(target, "LICENSE"), []byte(version.License), 0o600); err != nil {
		return err
	}
	for _, dir := range []string{filepath.Dir(source), filepath.Dir(filepath.Dir(source))} {
		notices, err := os.ReadFile(filepath.Join(dir, "THIRD_PARTY_LICENSES.md"))
		if err == nil {
			return os.WriteFile(filepath.Join(target, "THIRD_PARTY_LICENSES.md"), notices, 0o600)
		}
	}
	return nil
}

// existingVersion reports that a rename failed because the version directory already exists.
func existingVersion(err error, versionDir string) bool {
	if errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.ENOTEMPTY) {
		return true
	}
	if runtime.GOOS == "windows" {
		info, statErr := os.Lstat(versionDir)
		return statErr == nil && info.IsDir()
	}
	return false
}

func treePaths(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

// restrictTree: staged entries are never writable by group or others, so reuse can demand the
// same.
func restrictTree(root string) error {
	paths, err := treePaths(root)
	if err != nil {
		return err
	}
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := os.Chmod(path, info.Mode().Perm()&0o755); err != nil {
			return err
		}
	}
	return nil
}

func othersCanModify(root string) (bool, error) {
	if runtime.GOOS == "windows" {
		return false, nil
	}
	paths, err := treePaths(root)
	if err != nil {
		return false, err
	}
	uid := getuid()
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return false, err
		}
		if fileUID(info) != uid || info.Mode().Perm()&0o022 != 0 {
			return true, nil
		}
	}
	return false, nil
}

// treeDigest lists every non-directory entry as "<relative path>\0<sha256 or not-a-file>",
// sorted and joined with newlines.
func treeDigest(root string) (string, error) {
	paths, err := treePaths(root)
	if err != nil {
		return "", err
	}
	var files []string
	for _, path := range paths[1:] {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			continue
		}
		content := "not-a-file"
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			sum := sha256.Sum256(data)
			content = hex.EncodeToString(sum[:])
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		files = append(files, rel+"\x00"+content)
	}
	sort.Strings(files)
	return strings.Join(files, "\n"), nil
}

// assertSameRuntime: runtimes are named by their content; an existing one is reused only if it
// is still intact.
func assertSameRuntime(digest, installed string) error {
	modifiable, err := othersCanModify(installed)
	if err != nil {
		return err
	}
	current, err := treeDigest(installed)
	if err != nil {
		return err
	}
	if modifiable || current != digest {
		return clierror.New("The installed Durin runtime at " + installed + " does not match this CLI. Stop Durin bridges, remove that directory, then run durin again.")
	}
	return nil
}

// publishExecutable atomically replaces <runtime>/durin[.exe] with code. Windows cannot replace
// an executable that a running bridge has open, so there an identical copy is kept, and a
// different one is first moved aside (which Windows allows) and removed on a later install.
func publishExecutable(runtimeDir string, code []byte) (string, error) {
	published := filepath.Join(runtimeDir, RuntimeExecutableName())
	if runtime.GOOS == "windows" {
		removeRetired(runtimeDir)
		if current, err := os.ReadFile(published); err == nil && bytes.Equal(current, code) {
			return published, nil
		}
	}
	temporary := filepath.Join(runtimeDir, "durin-"+randomUUID()+".tmp")
	defer rmForce(temporary)
	if err := writeExclusive(temporary, code, 0o700); err != nil {
		return "", err
	}
	err := os.Rename(temporary, published)
	if err != nil && runtime.GOOS == "windows" {
		if moveErr := os.Rename(published, filepath.Join(runtimeDir, "durin-"+randomUUID()+".retired")); moveErr == nil {
			err = os.Rename(temporary, published)
		}
	}
	if err != nil {
		return "", err
	}
	return published, nil
}

func removeRetired(runtimeDir string) {
	retired, _ := filepath.Glob(filepath.Join(runtimeDir, "durin-*.retired"))
	for _, path := range retired {
		_ = os.Remove(path)
	}
}
