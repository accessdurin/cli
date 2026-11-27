// Package peercred answers one question about a connected Unix socket: does the process on
// the other end run as this user? It never hands out a user id, so a caller cannot write a
// check that lets a peer through when its credentials could not be read.
package peercred

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// SameUser returns nil only when the OS confirms that the peer of conn runs as this
// process's user. A connection that is not a Unix socket, a failed credential read and an OS
// with no peer-credential check are all errors.
func SameUser(conn net.Conn) error {
	uid, err := peerUID(conn)
	if err != nil {
		return fmt.Errorf("observer: cannot read the socket peer's user: %w", err)
	}
	if uid != os.Getuid() {
		return errors.New("observer: the socket is used by another user")
	}
	return nil
}

func unixConn(conn net.Conn) (*net.UnixConn, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("%T is not a Unix socket", conn)
	}
	return uc, nil
}
