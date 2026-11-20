//go:build windows

package observer

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const isWindows = true

const fileFlagFirstPipeInstance = 0x00080000

// currentUserSID is the SID of the user this process runs as.
func currentUserSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func dial(ctx context.Context, address string) (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return nil, err
	}
	for {
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			if err := ownedByCurrentUser(h); err != nil {
				windows.CloseHandle(h)
				return nil, err
			}
			return &pipeConn{h: h}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ownedByCurrentUser refuses a pipe another user created first under our name, so hook
// payloads only ever reach an observer running as this user.
func ownedByCurrentUser(h windows.Handle) error {
	info, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := info.Owner()
	if err != nil {
		return err
	}
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	if !owner.Equals(me) {
		return errors.New("observer: pipe is owned by another user")
	}
	return nil
}

// listen serves a named pipe whose ACL grants access to this user only, and rejects remote
// clients.
func listen(address string) (net.Listener, error) {
	me, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + me.String() + ")")
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return nil, err
	}
	l := &pipeListener{name: name, address: address, sa: &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, closed: make(chan struct{})}
	first, err := l.instance(fileFlagFirstPipeInstance)
	if err != nil {
		return nil, err
	}
	l.next = first
	return l, nil
}

type pipeListener struct {
	name    *uint16
	address string
	sa      *windows.SecurityAttributes
	mu      sync.Mutex
	next    windows.Handle
	closed  chan struct{}
	once    sync.Once
}

func (l *pipeListener) instance(extra uint32) (windows.Handle, error) {
	return windows.CreateNamedPipe(l.name, windows.PIPE_ACCESS_DUPLEX|extra,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, 64<<10, 64<<10, 0, l.sa)
}

func (l *pipeListener) Accept() (net.Conn, error) {
	for {
		l.mu.Lock()
		h := l.next
		l.mu.Unlock()
		err := windows.ConnectNamedPipe(h, nil)
		select {
		case <-l.closed:
			windows.CloseHandle(h)
			return nil, net.ErrClosed
		default:
		}
		next, nerr := l.instance(0)
		if nerr != nil {
			return nil, nerr
		}
		l.mu.Lock()
		l.next = next
		l.mu.Unlock()
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			windows.CloseHandle(h)
			continue
		}
		return &pipeConn{h: h, server: true}, nil
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		// Unblock a pending ConnectNamedPipe.
		if c, err := dial(context.Background(), l.address); err == nil {
			c.Close()
		}
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr(l.address) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// pipeConn is a synchronous pipe handle. Deadlines are not supported; callers bound their
// waits with contexts and goroutines instead.
type pipeConn struct {
	h      windows.Handle
	server bool
	once   sync.Once
}

func (c *pipeConn) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(c.h, b, &n, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) || (err == nil && n == 0 && len(b) > 0) {
		return int(n), errEOF
	}
	return int(n), err
}

func (c *pipeConn) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(c.h, b, &n, nil)
	return int(n), err
}

func (c *pipeConn) Close() error {
	c.once.Do(func() {
		if c.server {
			_ = windows.FlushFileBuffers(c.h)
			_ = windows.DisconnectNamedPipe(c.h)
		}
		windows.CloseHandle(c.h)
	})
	return nil
}

func (c *pipeConn) LocalAddr() net.Addr              { return pipeAddr("") }
func (c *pipeConn) RemoteAddr() net.Addr             { return pipeAddr("") }
func (c *pipeConn) SetDeadline(time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return nil }
