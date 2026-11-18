package observer

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procTable reads /proc/net/tcp{,6} and maps socket inodes to processes through
// /proc/<pid>/fd, only for the connections asked about, caching what it found.
type procTable struct {
	root   string
	inodes map[uint64]int
}

// NewSocketTable returns this OS's socket table.
func NewSocketTable() SocketTable { return &procTable{root: "/proc", inodes: map[uint64]int{}} }

func (t *procTable) Connections() ([]Conn, error) {
	var out []Conn
	var firstErr error
	for _, name := range []string{"net/tcp", "net/tcp6"} {
		f, err := os.Open(filepath.Join(t.root, name))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		conns, err := ParseProcNetTCP(f)
		f.Close()
		if err != nil && firstErr == nil {
			firstErr = err
		}
		out = append(out, conns...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	live := make(map[uint64]bool, len(out))
	for _, c := range out {
		live[c.Inode] = true
	}
	for inode := range t.inodes {
		if !live[inode] {
			delete(t.inodes, inode)
		}
	}
	return out, nil
}

func (t *procTable) ResolvePIDs(conns []Conn) []Conn {
	want := map[uint64]bool{}
	for _, c := range conns {
		if _, ok := t.inodes[c.Inode]; !ok && c.Inode != 0 {
			want[c.Inode] = true
		}
	}
	if len(want) > 0 {
		t.scan(want)
	}
	for i := range conns {
		conns[i].PID = t.inodes[conns[i].Inode]
	}
	return conns
}

// scan walks /proc/<pid>/fd until every wanted inode is found. Processes of other users are
// not readable and are skipped.
func (t *procTable) scan(want map[uint64]bool) {
	entries, err := os.ReadDir(t.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(t.root, e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode, err := strconv.ParseUint(strings.TrimSuffix(link[len("socket:["):], "]"), 10, 64)
			if err == nil && want[inode] {
				t.inodes[inode] = pid
				delete(want, inode)
				if len(want) == 0 {
					return
				}
			}
		}
	}
	for inode := range want {
		t.inodes[inode] = 0 // not ours to see; do not rescan for it
	}
}

type procInfo struct{ root string }

// NewProcessInfo returns this OS's process lookup.
func NewProcessInfo() ProcessInfo { return procInfo{"/proc"} }

func (p procInfo) Process(pid int) (Process, bool) {
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Process{}, false
	}
	out := Process{PID: pid}
	// The command name is parenthesized and may contain spaces; fields follow the last ')'.
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 {
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) > 19 {
			out.PPID, _ = strconv.Atoi(fields[1])
			out.Start, _ = strconv.ParseUint(fields[19], 10, 64)
		}
	}
	out.Exe, _ = os.Readlink(filepath.Join(dir, "exe"))
	if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(args) > 3 {
			args = args[:3]
		}
		out.Args = args
	}
	return out, true
}
