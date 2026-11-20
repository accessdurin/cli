//go:build !windows && !linux && !darwin

package observer

import "net"

func peerUID(net.Conn) (int, bool) { return 0, false }
