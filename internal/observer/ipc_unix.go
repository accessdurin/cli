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
)

const isWindows = false

func dial(ctx context.Context, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", address)
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

// peerChecked drops connections from other users. The directory and socket modes already
// keep them out; this holds even if those modes are changed.
type peerChecked struct{ net.Listener }

func (l peerChecked) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if uid, ok := peerUID(conn); ok && uid != os.Getuid() {
			conn.Close()
			continue
		}
		return conn, nil
	}
}
