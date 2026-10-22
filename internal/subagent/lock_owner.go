package subagent

import (
	"os"
	"syscall"

	"dyna-agent/internal/turnlock"
)

// runnerAlive checks the gate's runner identity; see turnlock.RunnerAlive.
func (s *Store) runnerAlive(id string) (alive, known bool) {
	dir, err := s.Dir(id)
	if err != nil {
		return false, false
	}
	return turnlock.RunnerAlive(dir, id)
}

// recoverTurnLock runs only under the session metadata lock, which also
// serializes turn allocation and all non-runner turn-lock guards. A guard
// must release its lock or prepare a queued gate before dropping metadata.
// Never replace a queued or live runner's lock.
func (s *Store) recoverTurnLock(id string) (*os.File, error) {
	lock, err := s.tryTurnLock(id)
	if err != ErrBusy {
		return lock, err
	}
	if alive, known := s.runnerAlive(id); alive || !known {
		return nil, err
	}
	path, err := s.path(id, "turn.lock")
	if err != nil {
		return nil, err
	}
	// TODO: legacy/shim remove later — old runners leaked turn.lock to descendants.
	// The confirmed former runner is gone. Descendants can retain the old inode,
	// but only this newly created inode will serialize future turns.
	if err := os.Remove(path); err != nil {
		return nil, err
	}
	s.appendEvent(id, map[string]any{"t": "orphan_turn_lock_recovered"})
	return s.tryTurnLock(id)
}

// protectLock prevents embedding callers from passing the owned lock onward.
func protectLock(lock *os.File) { syscall.CloseOnExec(int(lock.Fd())) }
