package roomsvc

import (
	"context"
	"errors"
	"os"
	"syscall"

	"dyna-agent/internal/callerid"
	"dyna-agent/internal/database"
	"dyna-agent/internal/runstore"
)

// CoordinatorChanged queues a canonical-record lookup, never work under a
// publisher's lock. An empty run asks for every registered coordinator.
func (s *Service) CoordinatorChanged(run string) {
	if run != "" && runstore.ValidateRunID(run) != nil {
		return
	}
	s.coordinatorMu.Lock()
	if s.coordinatorHints == nil {
		s.coordinatorHints = map[string]bool{}
	}
	s.coordinatorHints[run] = true
	s.coordinatorMu.Unlock()
	select {
	case s.coordinatorWake <- struct{}{}:
	default:
	}
}

func (s *Service) takeCoordinatorHints() map[string]bool {
	s.coordinatorMu.Lock()
	defer s.coordinatorMu.Unlock()
	hints := s.coordinatorHints
	s.coordinatorHints = nil
	return hints
}

type coordinatorCandidate struct {
	run     string
	process callerid.Process
}

// followCoordinators reads rows only; it never scans run or session folders.
// Terminal rows remain candidates until their process has finished cleanup.
func (w *runnerWatch) followCoordinators(ctx context.Context, hints map[string]bool) error {
	if w == nil || w.coordinatorExited == nil || len(hints) == 0 {
		return nil
	}
	db, err := database.Open(w.dataDir)
	if err != nil {
		return err
	}
	if hints[""] {
		hints = map[string]bool{"": true}
	}
	for run := range hints {
		if run != "" && runstore.ValidateRunID(run) != nil {
			continue
		}
		query := `SELECT DISTINCT c.run,c.pid,c.start FROM caller_records c
JOIN runs r ON r.id=c.run
WHERE c.actor='worker' AND c.session='' AND CAST(c.worker AS INTEGER)=0
AND c.pid>0 AND c.start>0 AND json_valid(CAST(r.body AS TEXT))
AND json_extract(CAST(r.body AS TEXT),'$.id')=r.id
AND json_extract(CAST(r.body AS TEXT),'$.pid')=c.pid`
		var args []any
		if run != "" {
			query += " AND c.run=?"
			args = append(args, run)
		}
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		var candidates []coordinatorCandidate
		for rows.Next() {
			var c coordinatorCandidate
			var start int64
			if err := rows.Scan(&c.run, &c.process.PID, &start); err != nil {
				rows.Close()
				return err
			}
			c.process.Start = uint64(start)
			if runstore.ValidateRunID(c.run) == nil {
				candidates = append(candidates, c)
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, c := range candidates {
			w.watchCoordinator(c)
		}
	}
	return nil
}

func (w *runnerWatch) watchCoordinator(c coordinatorCandidate) {
	w.mu.Lock()
	_, seen := w.coordinators[c.process]
	closed := w.closed
	w.mu.Unlock()
	if closed || seen {
		return
	}
	h, err := openRunner(c.process.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			w.coordinatorGone(c)
		}
		return
	}
	current, err := callerid.ProcessOf(c.process.PID)
	if err != nil {
		h.close()
		if errors.Is(err, os.ErrNotExist) {
			w.coordinatorGone(c)
		}
		return
	}
	if current != c.process || !w.recordedCoordinator(c) {
		h.close()
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		h.close()
		return
	}
	if _, seen := w.coordinators[c.process]; seen {
		w.mu.Unlock()
		h.close()
		return
	}
	w.coordinators[c.process] = struct{}{}
	w.done.Add(1)
	w.mu.Unlock()
	w.wait(h, func(exited bool) {
		if exited {
			w.coordinatorExited(c.run)
		}
	})
}

func (w *runnerWatch) recordedCoordinator(c coordinatorCandidate) bool {
	launch, ok, err := w.recorded(c.process)
	return err == nil && ok && launch.Kind == callerid.Worker && launch.Run == c.run && launch.Session == "" && launch.Agent == 0
}

// A verified hint can arrive after exit but before the pidfd opens. The
// canonical run/launch pair still permits receipt-only recovery, never launch.
func (w *runnerWatch) coordinatorGone(c coordinatorCandidate) {
	if !w.recordedCoordinator(c) {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	if _, seen := w.coordinators[c.process]; seen {
		w.mu.Unlock()
		return
	}
	w.coordinators[c.process] = struct{}{}
	w.mu.Unlock()
	w.coordinatorExited(c.run)
}
