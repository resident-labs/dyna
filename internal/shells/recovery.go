package shells

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"dyna-agent/internal/procident"
)

type supervisorHandle interface {
	Close()
	Kill() error
	Wait() error
}

// stopExitedSupervisor never signals the command's old process group. A
// pidfd pins the supervisor identity while its recorded start time is checked.
// A record without a matching start time fails closed.
func (s *Store) stopExitedSupervisor(in Info) error {
	if in.Supervisor <= 0 {
		return nil
	}
	fd, err := openSupervisor(in.Supervisor)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open supervisor process handle: %w", err)
	}
	defer fd.Close()
	started, err := procident.StartTime(in.Supervisor)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify supervisor identity: %w", err)
	}
	if started == 0 || started != in.SupervisorStartTime {
		return fmt.Errorf("cannot verify supervisor %d: process start time is missing or changed", in.Supervisor)
	}
	if in.Supervisor == os.Getpid() {
		return errors.New("refusing to stop the current process as an exited shell supervisor")
	}
	logErr := s.logStop(in.ID, "stopping exited shell supervisor; sending SIGKILL")
	err = fd.Kill()
	if errors.Is(err, syscall.ESRCH) {
		return logErr
	}
	if err != nil {
		return errors.Join(logErr, fmt.Errorf("stop exited shell supervisor: %w", err))
	}
	return errors.Join(logErr, fd.Wait())
}
