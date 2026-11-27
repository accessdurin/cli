//go:build !linux && !darwin

package peercred

import (
	"errors"
	"net"
)

func peerUID(net.Conn) (int, error) {
	return 0, errors.New("this OS has no peer-credential check")
}
