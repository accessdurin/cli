//go:build linux || darwin

package peercred

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A peer that runs as another user is refused. Changing user needs root, so this runs only
// as root; the client is this test binary re-run as nobody.
func TestSameUserRefusesAnotherUser(t *testing.T) {
	if address := os.Getenv("PEERCRED_DIAL"); address != "" {
		conn, err := net.Dial("unix", address)
		if err != nil {
			os.Exit(3)
		}
		_, _ = conn.Read(make([]byte, 1))
		os.Exit(0)
	}
	if os.Getuid() != 0 {
		t.Skip("needs root to run a client as another user")
	}
	dir, err := os.MkdirTemp("", "peer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	address := filepath.Join(dir, "s")
	l, err := net.Listen("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := os.Chmod(address, 0o777); err != nil {
		t.Fatal(err)
	}
	// The test binary's own directory is private to root, so nobody runs a copy.
	binary, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	client := filepath.Join(dir, "client")
	if err := os.WriteFile(client, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(client, "-test.run=^TestSameUserRefusesAnotherUser$")
	cmd.Env = append(os.Environ(), "PEERCRED_DIAL="+address)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	_ = l.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
	conn, err := l.Accept()
	if err != nil {
		t.Fatalf("the client running as nobody did not connect: %v", err)
	}
	defer conn.Close()
	if err := SameUser(conn); err == nil {
		t.Fatal("SameUser = nil for a peer running as nobody")
	}
}
