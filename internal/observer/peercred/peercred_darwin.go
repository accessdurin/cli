package peercred

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(conn net.Conn) (int, error) {
	uc, err := unixConn(conn)
	if err != nil {
		return 0, err
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED) }); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return int(cred.Uid), nil
}
