package subagent

import (
	"errors"
	"fmt"
	"syscall"

	"dyna-agent/internal/turnlock"
	"golang.org/x/sys/unix"
)

func signalRunner(id string, n, pid int, sig syscall.Signal) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// Pin the process before reading its command line. A reused pid can
	// never receive this signal, and the identity must name this exact turn.
	if alive, known := turnlock.RunnerMatches(pid, id, n); !known || !alive {
		return fmt.Errorf("refuse to signal pid %d: it is not runner %s turn %d", pid, id, n)
	}
	err = unix.PidfdSendSignal(fd, sig, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}
