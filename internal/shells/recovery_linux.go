//go:build linux

package shells

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

type pidfdHandle int

func openSupervisor(pid int) (supervisorHandle, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	return pidfdHandle(fd), err
}

func (fd pidfdHandle) Close() { _ = unix.Close(int(fd)) }

func (fd pidfdHandle) Kill() error {
	return unix.PidfdSendSignal(int(fd), unix.SIGKILL, nil, 0)
}

func (fd pidfdHandle) Wait() error {
	for deadline := time.Now().Add(5 * time.Second); ; {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("exited shell supervisor did not stop within 5 seconds")
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(remaining.Milliseconds())+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for exited shell supervisor: %w", err)
		}
		if n > 0 {
			return nil
		}
	}
}
