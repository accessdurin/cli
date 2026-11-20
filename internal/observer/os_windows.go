package observer

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
)

const tcpTableOwnerPIDAll = 5

// tcpTable reads the TCP tables with GetExtendedTcpTable, which needs no administrator
// rights.
type tcpTable struct{}

// NewSocketTable returns this OS's socket table.
func NewSocketTable() SocketTable { return tcpTable{} }

func extendedTCPTable(family uint32) ([]byte, error) {
	size := uint32(16 << 10)
	for range 5 {
		buf := make([]byte, size)
		r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:size], nil
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4 << 10
			continue
		default:
			return nil, windows.Errno(r)
		}
	}
	return nil, errors.New("observer: TCP table keeps growing")
}

func (tcpTable) Connections() ([]Conn, error) {
	var out []Conn
	v4, err4 := extendedTCPTable(windows.AF_INET)
	if err4 == nil {
		out = append(out, ParseTCPTableOwnerPID(v4)...)
	}
	v6, err6 := extendedTCPTable(windows.AF_INET6)
	if err6 == nil {
		out = append(out, ParseTCP6TableOwnerPID(v6)...)
	}
	if err4 != nil && err6 != nil {
		return nil, err4
	}
	return out, nil
}

type windowsInfo struct{}

// NewProcessInfo returns this OS's process lookup.
func NewProcessInfo() ProcessInfo { return windowsInfo{} }

func (windowsInfo) Process(pid int) (Process, bool) {
	out := Process{PID: pid}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return out, false
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
		out.Exe = windows.UTF16ToString(buf[:size])
	}
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &exit, &kernel, &user) == nil {
		out.Start = uint64(creation.Nanoseconds())
	}
	out.PPID = parentPID(uint32(pid))
	return out, true
}

func parentPID(pid uint32) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == pid {
			return int(entry.ParentProcessID)
		}
	}
	return 0
}
