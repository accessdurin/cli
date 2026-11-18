//go:build !linux

package observer

import (
	"os"
	"runtime"
)

func osGetpid() int { return os.Getpid() }

// processRSS approximates resident memory with what the Go runtime obtained from the OS.
func processRSS() (current, peak uint64) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return mem.Sys - mem.HeapReleased, mem.Sys
}
