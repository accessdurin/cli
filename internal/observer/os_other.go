//go:build !linux && !darwin && !windows

package observer

import "errors"

type noTable struct{}

// NewSocketTable returns this OS's socket table (none here).
func NewSocketTable() SocketTable { return noTable{} }

func (noTable) Connections() ([]Conn, error) {
	return nil, errors.New("observer: reading the socket table is not supported on this OS")
}

type noInfo struct{}

// NewProcessInfo returns this OS's process lookup (none here).
func NewProcessInfo() ProcessInfo { return noInfo{} }

func (noInfo) Process(int) (Process, bool) { return Process{}, false }
