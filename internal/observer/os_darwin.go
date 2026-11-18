package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// lsofTable lists TCP sockets with lsof, which needs no administrator rights for this
// user's processes. (libproc would avoid the subprocess but needs cgo, which the
// cross-compiled release builds do not use.)
type lsofTable struct{}

// NewSocketTable returns this OS's socket table.
func NewSocketTable() SocketTable { return lsofTable{} }

func (lsofTable) Connections() ([]Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-w", "-FpcnT").Output()
	if err != nil && len(out) == 0 {
		// lsof exits 1 when nothing matched.
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return ParseLsof(bytes.NewReader(out)), nil
}

type darwinInfo struct{}

// NewProcessInfo returns this OS's process lookup.
func NewProcessInfo() ProcessInfo { return darwinInfo{} }

func (darwinInfo) Process(pid int) (Process, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return Process{}, false
	}
	out := Process{PID: pid, PPID: int(kp.Eproc.Ppid), Start: uint64(kp.Proc.P_starttime.Sec)*1e6 + uint64(kp.Proc.P_starttime.Usec)}
	// kern.procargs2: argc (int32), the executable path, NULs, then argv.
	if raw, err := unix.SysctlRaw("kern.procargs2", pid); err == nil && len(raw) > 4 {
		argc := int(binary.LittleEndian.Uint32(raw))
		rest := raw[4:]
		if i := bytes.IndexByte(rest, 0); i >= 0 {
			out.Exe = string(rest[:i])
			rest = bytes.TrimLeft(rest[i:], "\x00")
		}
		for len(out.Args) < argc && len(out.Args) < 3 && len(rest) > 0 {
			i := bytes.IndexByte(rest, 0)
			if i < 0 {
				i = len(rest)
			}
			out.Args = append(out.Args, string(rest[:i]))
			rest = rest[min(i+1, len(rest)):]
		}
	}
	return out, true
}
