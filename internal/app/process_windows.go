package app

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// processMissing is libuv's uv_kill(pid, 0) failing with ESRCH: the process cannot be opened
// because it does not exist, or it has already exited.
func processMissing(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if windows.GetExitCodeProcess(handle, &code) != nil {
		return false
	}
	return code != 259 // STILL_ACTIVE
}

// executable is fs.access(path, X_OK) succeeding, which on Windows only requires the path to
// exist.
func executable(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// hideWindow is spawn's windowsHide: the child gets no console window.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
