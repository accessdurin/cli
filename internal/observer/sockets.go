package observer

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
)

// Conn is one TCP connection from the OS socket table.
type Conn struct {
	Local  netip.AddrPort
	Remote netip.AddrPort
	State  string // established | syn-sent
	PID    int    // 0 when not known yet
	Inode  uint64 // Linux only
}

// SocketTable reads the current TCP connections. Implementations need no administrator
// rights and therefore see the connections of processes this user may inspect.
type SocketTable interface {
	Connections() ([]Conn, error)
}

// PIDResolver is implemented by tables that attribute connections to processes lazily
// (Linux maps socket inodes to processes only for connections that matter).
type PIDResolver interface {
	ResolvePIDs(conns []Conn) []Conn
}

var linuxStates = map[string]string{"01": "established", "02": "syn-sent"}

// ParseProcNetTCP parses /proc/net/tcp or /proc/net/tcp6, keeping established and
// connecting sockets.
func ParseProcNetTCP(r io.Reader) ([]Conn, error) {
	var out []Conn
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		state, ok := linuxStates[fields[3]]
		if !ok {
			continue
		}
		local, err1 := parseProcAddr(fields[1])
		remote, err2 := parseProcAddr(fields[2])
		inode, err3 := strconv.ParseUint(fields[9], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, Conn{Local: local, Remote: remote, State: state, Inode: inode})
	}
	return out, scanner.Err()
}

// parseProcAddr parses "0100007F:1F90": the address as 32-bit words in host (little-endian)
// order, the port in hex.
func parseProcAddr(s string) (netip.AddrPort, error) {
	host, port, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, errors.New("bad address")
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, errors.New("bad address")
	}
	for i := 0; i < len(raw); i += 4 {
		binary.BigEndian.PutUint32(raw[i:], binary.LittleEndian.Uint32(raw[i:]))
	}
	p, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(p)), nil
}

// ParseLsof parses `lsof -nP -iTCP -FpcnT` output (macOS): p<pid>, c<command>, then per
// socket n<local>-><remote> and T fields including TST=<state>.
func ParseLsof(r io.Reader) []Conn {
	var out []Conn
	pid := 0
	var pending *Conn
	flush := func() {
		if pending != nil && pending.State != "" {
			out = append(out, *pending)
		}
		pending = nil
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			flush()
			pid, _ = strconv.Atoi(line[1:])
		case 'f':
			flush()
		case 'n':
			flush()
			local, remote, ok := strings.Cut(line[1:], "->")
			if !ok {
				continue
			}
			l, err1 := parseHostPort(local)
			rem, err2 := parseHostPort(remote)
			if err1 != nil || err2 != nil {
				continue
			}
			pending = &Conn{Local: l, Remote: rem, PID: pid}
		case 'T':
			if pending != nil && strings.HasPrefix(line, "TST=") {
				switch line[4:] {
				case "ESTABLISHED":
					pending.State = "established"
				case "SYN_SENT":
					pending.State = "syn-sent"
				}
			}
		}
	}
	flush()
	return out
}

func parseHostPort(s string) (netip.AddrPort, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return netip.AddrPort{}, errors.New("bad address")
	}
	host := strings.TrimSuffix(strings.TrimPrefix(s[:i], "["), "]")
	if j := strings.IndexByte(host, '%'); j >= 0 {
		host = host[:j]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, err
	}
	port, err := strconv.ParseUint(s[i+1:], 10, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)), nil
}

// Windows MIB_TCP_STATE values.
const (
	mibTCPStateSynSent     = 3
	mibTCPStateEstablished = 5
)

func windowsState(s uint32) string {
	switch s {
	case mibTCPStateEstablished:
		return "established"
	case mibTCPStateSynSent:
		return "syn-sent"
	}
	return ""
}

// ParseTCPTableOwnerPID parses a MIB_TCPTABLE_OWNER_PID buffer from GetExtendedTcpTable
// (AF_INET, TCP_TABLE_OWNER_PID_ALL): dwNumEntries, then rows of six DWORDs (state, local
// address, local port, remote address, remote port, owning pid). Addresses are in network
// order; ports are in network order in the low 16 bits.
func ParseTCPTableOwnerPID(buf []byte) []Conn {
	if len(buf) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(buf))
	var out []Conn
	for i := 0; i < n; i++ {
		row := buf[4+i*24:]
		if len(row) < 24 {
			break
		}
		state := windowsState(binary.LittleEndian.Uint32(row[0:]))
		if state == "" {
			continue
		}
		local := netip.AddrFrom4([4]byte(row[4:8]))
		remote := netip.AddrFrom4([4]byte(row[12:16]))
		out = append(out, Conn{
			Local:  netip.AddrPortFrom(local, binary.BigEndian.Uint16(row[8:10])),
			Remote: netip.AddrPortFrom(remote, binary.BigEndian.Uint16(row[16:18])),
			State:  state,
			PID:    int(binary.LittleEndian.Uint32(row[20:])),
		})
	}
	return out
}

// ParseTCP6TableOwnerPID parses a MIB_TCP6TABLE_OWNER_PID buffer (AF_INET6): rows of local
// address [16], local scope id, local port, remote address [16], remote scope id, remote
// port, state, owning pid (56 bytes).
func ParseTCP6TableOwnerPID(buf []byte) []Conn {
	if len(buf) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(buf))
	var out []Conn
	for i := 0; i < n; i++ {
		row := buf[4+i*56:]
		if len(row) < 56 {
			break
		}
		state := windowsState(binary.LittleEndian.Uint32(row[48:]))
		if state == "" {
			continue
		}
		local := netip.AddrFrom16([16]byte(row[0:16])).Unmap()
		remote := netip.AddrFrom16([16]byte(row[24:40])).Unmap()
		out = append(out, Conn{
			Local:  netip.AddrPortFrom(local, binary.BigEndian.Uint16(row[20:22])),
			Remote: netip.AddrPortFrom(remote, binary.BigEndian.Uint16(row[44:46])),
			State:  state,
			PID:    int(binary.LittleEndian.Uint32(row[52:])),
		})
	}
	return out
}
