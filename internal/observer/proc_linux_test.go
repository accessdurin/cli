package observer

import (
	"net"
	"net/netip"
	"os"
	"testing"
)

// On Linux the real /proc table finds this process's own connection to a listener.
func TestProcTableFindsOwnConnection(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	table := NewSocketTable().(*procTable)
	conns, err := table.Connections()
	if err != nil {
		t.Fatal(err)
	}
	remote := netip.MustParseAddrPort(l.Addr().String())
	var mine []Conn
	for _, c := range conns {
		if c.Remote == remote {
			mine = append(mine, c)
		}
	}
	mine = table.ResolvePIDs(mine)
	if len(mine) != 1 || mine[0].PID != os.Getpid() {
		t.Fatalf("%+v", mine)
	}
	p, ok := NewProcessInfo().Process(os.Getpid())
	if !ok || p.PPID != os.Getppid() || p.Exe == "" || p.Start == 0 {
		t.Fatalf("%+v", p)
	}
}
