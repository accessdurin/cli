package observer

import (
	"os"
	"strconv"
	"strings"
)

func osGetpid() int { return os.Getpid() }

// processRSS reads VmRSS and VmHWM from /proc/self/status.
func processRSS() (current, peak uint64) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, _ := strconv.ParseUint(fields[0], 10, 64)
		switch key {
		case "VmRSS":
			current = kb << 10
		case "VmHWM":
			peak = kb << 10
		}
	}
	return current, peak
}
