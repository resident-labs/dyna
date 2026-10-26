//go:build unix

package shellmcp

import (
	"os"

	"golang.org/x/sys/unix"
)

// wakeup is a pipe that interrupts a poll on stdin.
type wakeup interface {
	signal()
	// wait blocks until in has bytes to read or the wake-up fires. It returns
	// true when the input is ready, and false after a wake-up.
	wait(in *os.File) bool
	close()
}

type pipeWakeup struct{ r, w int }

func newWakeup() wakeup {
	var fds [2]int
	if err := unix.Pipe(fds[:]); err != nil {
		return nil
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
		if err := unix.SetNonblock(fd, true); err != nil {
			_ = unix.Close(fds[0])
			_ = unix.Close(fds[1])
			return nil
		}
	}
	return &pipeWakeup{r: fds[0], w: fds[1]}
}

func (p *pipeWakeup) signal() { _, _ = unix.Write(p.w, []byte{1}) }

func (p *pipeWakeup) close() {
	_ = unix.Close(p.r)
	_ = unix.Close(p.w)
}

func (p *pipeWakeup) wait(in *os.File) bool {
	fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}, {Fd: int32(p.r), Events: unix.POLLIN}}
	for {
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			// Poll cannot watch this input; read it as usual.
			return true
		}
		break
	}
	if fds[1].Revents != 0 {
		var drain [64]byte
		for {
			if n, err := unix.Read(p.r, drain[:]); n <= 0 || err != nil {
				break
			}
		}
	}
	// Bytes, end of input or an error on stdin all end the wait.
	return fds[0].Revents != 0
}
