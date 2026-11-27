//go:build !windows

package observer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/accessdurin/cli/internal/observer/peercred"
)

const isWindows = false

// dial connects only to an observer running as this user, through a socket this user owns in
// a directory no other user can enter. The temporary-directory fallback address is in a
// shared directory where another user could create it first. Unlike listen, dial never
// changes a mode.
func dial(ctx context.Context, address string) (net.Conn, error) {
	if err := privateToUser(filepath.Dir(address), true); err != nil {
		return nil, err
	}
	if err := privateToUser(address, false); err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", address)
	if err != nil {
		return nil, err
	}
	if err := peercred.SameUser(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// privateToUser refuses anything but this user's own socket, or this user's own directory
// without group or other permissions. Symlinks are refused.
func privateToUser(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	kind := info.Mode().Type() == os.ModeSocket
	if dir {
		kind = info.IsDir() && info.Mode().Perm()&0o077 == 0
	}
	if !kind || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("observer: %s is not private to this user", path)
	}
	return nil
}

// listen creates the socket in an owner-only directory and makes the socket itself
// owner-only. A stale socket from a crashed observer is replaced; a live one is an error, so
// that two observers never share an address.
func listen(address string) (net.Listener, error) {
	dir := filepath.Dir(address)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := ownerOnlyDirectory(dir); err != nil {
		return nil, err
	}
	if conn, err := net.Dial("unix", address); err == nil {
		conn.Close()
		return nil, errors.New("observer: another observer is already listening")
	}
	_ = os.Remove(address)
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", address)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(address, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	l.(*net.UnixListener).SetUnlinkOnClose(true)
	return peerChecked{l}, nil
}

func ownerOnlyDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("observer: %s is not a directory owned by this user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// peerChecked drops connections from other users and from peers it cannot identify. The
// directory and socket modes already keep other users out; this holds even if those modes
// are changed.
type peerChecked struct{ net.Listener }

func (l peerChecked) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if peercred.SameUser(conn) != nil {
			conn.Close()
			continue
		}
		return conn, nil
	}
}
