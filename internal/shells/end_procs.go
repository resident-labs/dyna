package shells

import (
	"errors"
	"fmt"

	"dyna-agent/internal/mode"
)

// KillFunc ends the process groups that hold a process stamped with stamp (an
// exact "NAME=value" environment assignment), and the group led by leader
// (0 for none). It spares the groups in keep and any group that holds a
// process of a job in keepJobs. runprocs.KillStamped is the real one; tests
// pass a fake.
type KillFunc func(stamp string, leader int, keep map[int]bool, keepJobs []string) error

// EndRunProcesses ends everything workers of runID started, the way a cancel
// does: non-keep shell jobs stop through their supervisors, kept jobs stay
// alive, and every other stamped process group is killed. leader is the run
// coordinator's group, or 0 to spare it. A caller that runs inside the
// coordinator's own finish call passes 0, and the coordinator carries no run
// stamp, so it is never selected.
func (s *Store) EndRunProcesses(runID string, leader int, kill KillFunc) error {
	if runID == "" {
		return errors.New("a run id is required")
	}
	var errs []error
	if err := s.StopRun(runID); err != nil {
		errs = append(errs, fmt.Errorf("stopping its shell jobs: %w", err))
	}
	keep, keepJobs, err := s.keptJobs(func(in Info) bool { return in.Run == runID })
	if err != nil {
		errs = append(errs, err)
	}
	if err := kill(mode.EnvWorkerRun+"="+runID, leader, keep, keepJobs); err != nil {
		errs = append(errs, fmt.Errorf("ending its processes: %w", err))
	}
	return errors.Join(errs...)
}

// EndSessionProcesses ends the processes of one closed session, and only that
// session's: those stamped with its DYNA_SESSION. Kept jobs of the session
// survive. Its non-keep jobs are already stopped by the close.
func (s *Store) EndSessionProcesses(session string, kill KillFunc) error {
	if session == "" {
		return errors.New("a session id is required")
	}
	keep, keepJobs, err := s.keptJobs(func(in Info) bool { return in.Owner == session })
	errs := []error{err}
	if err := kill(mode.EnvSession+"="+session, 0, keep, keepJobs); err != nil {
		errs = append(errs, fmt.Errorf("ending its processes: %w", err))
	}
	return errors.Join(errs...)
}

// keptJobs returns the groups and job identities of the kept jobs that match,
// which a kill must spare.
func (s *Store) keptJobs(match func(Info) bool) (map[int]bool, []string, error) {
	keep := map[int]bool{}
	var keepJobs []string
	list, err := s.List(false)
	if err != nil {
		return keep, nil, fmt.Errorf("listing its shell jobs: %w", err)
	}
	for _, in := range list {
		if match(in) && in.Keep {
			keep[in.Pid], keep[in.Supervisor] = true, true
			// A child that left the job's group still carries its identity.
			keepJobs = append(keepJobs, JobEnv+"="+in.ID)
		}
	}
	return keep, keepJobs, nil
}
