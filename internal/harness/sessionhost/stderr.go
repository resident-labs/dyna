package sessionhost

import (
	"os"
	"sync"
	"syscall"
)

// stderrPipe owns the read side, so a diagnostic request can consume bytes
// already in the pipe even when the background reader has not run yet.
type stderrPipe struct {
	reader, writer *os.File
	raw            syscall.RawConn
	window         *stderrWindow
	mu             sync.Mutex
	done           chan struct{}
}

func newStderrPipe(window *stderrWindow) (*stderrPipe, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	raw, err := reader.SyscallConn()
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	return &stderrPipe{reader: reader, writer: writer, raw: raw, window: window, done: make(chan struct{})}, nil
}

// read holds the same lock for consuming and recording bytes. A drain cannot
// miss bytes consumed by the background reader but not yet added to the tail.
func (p *stderrPipe) read(fd uintptr) (int, error) {
	buf := make([]byte, 32<<10)
	var n int
	var err error
	for {
		n, err = syscall.Read(int(fd), buf)
		if err != syscall.EINTR {
			break
		}
	}
	if n > 0 {
		_, _ = p.window.Write(buf[:n])
	}
	return n, err
}

func (p *stderrPipe) start() {
	go func() {
		defer close(p.done)
		for {
			var n int
			var err error
			pollErr := p.raw.Read(func(fd uintptr) bool {
				p.mu.Lock()
				defer p.mu.Unlock()
				n, err = p.read(fd)
				return err != syscall.EAGAIN && err != syscall.EWOULDBLOCK
			})
			if pollErr != nil || err != nil || n == 0 {
				return
			}
		}
	}()
}

// drain reaches the pipe's current end without waiting for the retained child
// to exit. Bytes written before a final stdout reply are included in its tail.
func (p *stderrPipe) drain() {
	_ = p.raw.Control(func(fd uintptr) {
		p.mu.Lock()
		defer p.mu.Unlock()
		for {
			n, err := p.read(fd)
			if err != nil || n == 0 {
				return
			}
		}
	})
}

func (p *stderrPipe) finish() {
	p.drain()
	_ = p.reader.Close()
	<-p.done
}
