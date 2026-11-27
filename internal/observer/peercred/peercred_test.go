package peercred

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// SameUser vouches for a peer only when the OS has confirmed its user. Every way the
// credentials can be unreadable is a refusal, never a pass (MAR-1891 N5).
func TestSameUserRefusesPeersItCannotRead(t *testing.T) {
	dir, err := os.MkdirTemp("", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	err = SameUser(client)
	if supported := runtime.GOOS == "linux" || runtime.GOOS == "darwin"; supported != (err == nil) {
		t.Fatalf("%s: SameUser on a socket to this process = %v", runtime.GOOS, err)
	}

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tcpConn, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcpConn.Close()
	pipe, other := net.Pipe()
	defer other.Close()
	client.Close()
	for name, conn := range map[string]net.Conn{"closed Unix socket": client, "TCP": tcpConn, "in-memory pipe": pipe} {
		if err := SameUser(conn); err == nil {
			t.Errorf("%s: SameUser = nil for a peer whose user it cannot read", name)
		}
	}
}
