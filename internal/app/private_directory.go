//go:build !windows

package app

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/accessdurin/cli/internal/clierror"
)

// statInfo is the part of a Node fs.Stats the directory checks read.
type statInfo struct {
	uid  uint32
	mode uint32
}

func (s statInfo) isSymlink() bool { return s.mode&unix.S_IFMT == unix.S_IFLNK }
func (s statInfo) isDir() bool     { return s.mode&unix.S_IFMT == unix.S_IFDIR }

func lstatInfo(path string) (statInfo, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return statInfo{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return statInfo{uid: st.Uid, mode: uint32(st.Mode)}, nil
}

func statPath(path string) (statInfo, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return statInfo{}, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	return statInfo{uid: st.Uid, mode: uint32(st.Mode)}, nil
}

// withAncestors is the path and each directory above it, up to the root.
func withAncestors(path string) []string {
	parent := filepath.Dir(path)
	if parent == path {
		return []string{path}
	}
	return append([]string{path}, withAncestors(parent)...)
}

// replaceableByOthers: another user can swap a symlink they own, or any entry of a
// world-writable non-sticky directory.
func replaceableByOthers(info statInfo, uid uint32) bool {
	if info.isSymlink() {
		return info.uid != uid && info.uid != 0
	}
	return info.mode&0o1002 == 0o002
}

func unsafeReason(info statInfo) string {
	if info.isSymlink() {
		return "is a symlink owned by another user"
	}
	return "is world-writable without the sticky bit"
}

func assertSafePath(directory string, uid uint32) error {
	real, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	paths := append(withAncestors(directory), withAncestors(real)...)
	infos := make([]statInfo, len(paths))
	for i, path := range paths {
		if infos[i], err = lstatInfo(path); err != nil {
			return err
		}
	}
	for i, info := range infos {
		if replaceableByOthers(info, uid) {
			return clierror.New(paths[i] + ", on the path to the Durin configuration directory, " + unsafeReason(info) + ", so another user could replace it. Choose another --config-dir.")
		}
	}
	return nil
}

// getuid is process.getuid; tests replace it.
var getuid = func() uint32 { return uint32(os.Getuid()) }

// AssertPrivateDirectory: agents execute the runtime stored here, so other local users must
// not control it. A missing directory is fine; it is created privately when needed.
func AssertPrivateDirectory(directory string) error {
	uid := getuid()
	info, err := statPath(directory)
	if err != nil {
		if missing(err) {
			return nil
		}
		return err
	}
	if !info.isDir() || info.uid != uid || info.mode&0o077 != 0 {
		return clierror.New("The Durin directory " + directory + " must be a directory owned by you that only you can access. Run chmod 700 on it, or choose another --config-dir.")
	}
	return assertSafePath(directory, uid)
}
