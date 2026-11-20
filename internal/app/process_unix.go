//go:build !windows

package app

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// processMissing is process.kill(pid, 0) failing with ESRCH.
func processMissing(pid int) bool {
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH)
}

// executable is fs.access(path, X_OK) succeeding.
func executable(path string) bool { return unix.Access(path, unix.X_OK) == nil }

// hideWindow is spawn's windowsHide, which does nothing outside Windows.
func hideWindow(*exec.Cmd) {}

// getuid is process.getuid; tests replace it.
var getuid = func() uint32 { return uint32(os.Getuid()) }

// detach is spawn's detached: the child gets its own session, so it outlives the hook.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &unix.SysProcAttr{Setsid: true}
}
