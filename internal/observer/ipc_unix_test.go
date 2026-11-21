//go:build !windows

package observer

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A hook sends only to a socket this user owns, in a directory no other user can enter. The
// temporary-directory fallback lives in a shared directory, where another user can create
// durin-<uid> first and listen in it; the directory the victim could reach that way has
// group/other bits, or is reached through a symlink.
func TestSendRefusesSocketOtherUsersCouldPlant(t *testing.T) {
	mkdir := func(t *testing.T, path string, mode os.FileMode) string {
		t.Helper()
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, c := range []struct {
		name string
		// plant returns the socket a listener binds and the address a hook dials.
		plant func(t *testing.T, root string) (socket, address string)
	}{
		{"shared directory", func(t *testing.T, root string) (string, string) {
			socket := filepath.Join(mkdir(t, filepath.Join(root, "durin"), 0o777), "o.sock")
			return socket, socket
		}},
		{"symlinked directory", func(t *testing.T, root string) (string, string) {
			real := mkdir(t, filepath.Join(root, "real"), 0o700)
			if err := os.Symlink(real, filepath.Join(root, "durin")); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(real, "o.sock"), filepath.Join(root, "durin", "o.sock")
		}},
		{"symlinked socket", func(t *testing.T, root string) (string, string) {
			socket := filepath.Join(mkdir(t, filepath.Join(root, "real"), 0o700), "o.sock")
			address := filepath.Join(mkdir(t, filepath.Join(root, "durin"), 0o700), "o.sock")
			if err := os.Symlink(socket, address); err != nil {
				t.Fatal(err)
			}
			return socket, address
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			socket, address := c.plant(t, shortDir(t))
			l, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := Send(ctx, address, Message{Event: "UserPromptSubmit", Payload: EventPayload([]byte(`{"prompt":"secret"}`))}); err == nil {
				t.Fatal("sent a hook payload to a socket another user could have planted")
			}
			_ = l.(*net.UnixListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
			if conn, err := l.Accept(); err == nil {
				conn.Close()
				t.Fatal("the planted listener received a connection")
			}
		})
	}
}
