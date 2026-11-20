//go:build !windows

package app

import (
	"os"
	"syscall"
)

// fileUID is fs.Stats#uid.
func fileUID(info os.FileInfo) uint32 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	return 0
}
