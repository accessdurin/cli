package app

import (
	"io"
	"os"
	"sync"
)

// promptInput hands the terminal's input to one question at a time.
//
// Bubble Tea reads a terminal through muesli/cancelreader, whose epoll (Linux) and kqueue
// (BSD, macOS) readers race on shutdown: the program cancels and closes the reader while its
// read goroutine can still be waiting on the old poll instance, which keeps watching stdin.
// That goroutine then wakes on the next question's first keystrokes, reads them and drops
// them, so an answer typed promptly is sometimes lost. Instead, one goroutine reads the
// terminal for the whole run, and each question gets a view of it (promptSession) that stops
// reading, without consuming anything, once the question is over.
type promptInput struct {
	file     *os.File
	start    sync.Once
	chunks   chan []byte
	mu       sync.Mutex
	leftover []byte
}

func newPromptInput(file *os.File) *promptInput {
	return &promptInput{file: file, chunks: make(chan []byte)}
}

func (in *promptInput) read() {
	for {
		buffer := make([]byte, 4096)
		n, err := in.file.Read(buffer)
		if n > 0 {
			in.chunks <- buffer[:n]
		}
		if err != nil {
			close(in.chunks)
			return
		}
	}
}

// session is the input for one question; Close it once the question is answered.
func (in *promptInput) session() *promptSession {
	in.start.Do(func() { go in.read() })
	return &promptSession{in: in, done: make(chan struct{})}
}

// unread returns bytes for the next reader.
func (in *promptInput) unread(b []byte) {
	in.mu.Lock()
	in.leftover = append(append([]byte(nil), b...), in.leftover...)
	in.mu.Unlock()
}

// promptSession is a terminal for Bubble Tea (it has the descriptor, so the program puts the
// terminal in raw mode) that cancelreader cannot poll (it has no Name), so the program reads
// it with plain Read calls, which end once the session is closed.
type promptSession struct {
	in   *promptInput
	done chan struct{}
	once sync.Once
}

func (s *promptSession) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *promptSession) Read(b []byte) (int, error) {
	if s.closed() {
		return 0, io.EOF
	}
	s.in.mu.Lock()
	if len(s.in.leftover) > 0 {
		n := copy(b, s.in.leftover)
		s.in.leftover = s.in.leftover[n:]
		s.in.mu.Unlock()
		return n, nil
	}
	s.in.mu.Unlock()
	select {
	case <-s.done:
		return 0, io.EOF
	case chunk, ok := <-s.in.chunks:
		if !ok {
			return 0, io.EOF
		}
		if s.closed() {
			s.in.unread(chunk)
			return 0, io.EOF
		}
		n := copy(b, chunk)
		if n < len(chunk) {
			s.in.unread(chunk[n:])
		}
		return n, nil
	}
}

func (s *promptSession) Write(b []byte) (int, error) { return s.in.file.Write(b) }

func (s *promptSession) Fd() uintptr { return s.in.file.Fd() }

// Close ends the session; a Read in progress returns io.EOF.
func (s *promptSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
