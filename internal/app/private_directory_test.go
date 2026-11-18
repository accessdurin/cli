//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDirectory(t *testing.T) {
	root := tempDir(t)
	if err := AssertPrivateDirectory(filepath.Join(root, "missing")); err != nil {
		t.Fatalf("a missing directory is fine: %v", err)
	}
	config := filepath.Join(root, "config")
	if err := os.Mkdir(config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := AssertPrivateDirectory(config); err != nil {
		t.Fatalf("a private directory is fine: %v", err)
	}
	for _, mode := range []os.FileMode{0o777, 0o770, 0o757, 0o755, 0o750, 0o705} {
		_ = os.Chmod(config, mode)
		wantCliError(t, AssertPrivateDirectory(config), "chmod 700")
	}
	_ = os.Chmod(config, 0o700)

	writeFile(t, filepath.Join(root, "file"), "")
	wantCliError(t, AssertPrivateDirectory(filepath.Join(root, "file")), "owned by you")
	uid := getuid
	getuid = func() uint32 { return uid() + 1 }
	err := AssertPrivateDirectory(config)
	getuid = uid
	wantCliError(t, err, "owned by you")

	nested := filepath.Join(root, "shared", "config")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(root, "shared"), 0o777)
	wantCliError(t, AssertPrivateDirectory(nested), filepath.Join(root, "shared")+", on the path to the Durin configuration directory, is world-writable without the sticky bit")
	_ = os.Chmod(filepath.Join(root, "shared"), 0o777|os.ModeSticky)
	if err := AssertPrivateDirectory(nested); err != nil {
		t.Fatalf("a sticky shared directory is fine: %v", err)
	}
}

func TestReplaceableByOthers(t *testing.T) {
	link := func(uid uint32) statInfo { return statInfo{uid: uid, mode: 0o120777} }
	dir := func(mode uint32) statInfo { return statInfo{uid: 501, mode: mode} }
	cases := []struct {
		info statInfo
		want bool
	}{
		{link(501), false}, {link(0), false}, {link(4242), true},
		{dir(0o40777), true}, {dir(0o41777), false}, {dir(0o40775), false},
	}
	for _, c := range cases {
		if got := replaceableByOthers(c.info, 501); got != c.want {
			t.Errorf("%+v: got %v", c.info, got)
		}
	}
}
