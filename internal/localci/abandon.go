package localci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RunID names one local CI run: the run folder name, the first twelve hex
// characters of the commit, a dash, and the start time in nanoseconds.
type RunID string

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{12}-[0-9]+$`)

// ParseRunID checks a run ID that an owner or a command gave. Callers pass the
// result on as a RunID, so the check runs once.
func ParseRunID(s string) (RunID, error) {
	if !runIDPattern.MatchString(s) {
		return "", fmt.Errorf("run ID %q is not a local CI run ID: want 12 hex characters, a dash and digits", s)
	}
	return RunID(s), nil
}

// ErrRunLive reports that the process which executes a run still runs, so the
// run is not stale.
var ErrRunLive = errors.New("local CI run is live")

// ErrRunUnclaimed reports that a kept run's record cannot be taken over: it is
// past the attachment TTL, or another process claimed it first. Nothing is
// released for such a run, because its checks may still hold the machine.
var ErrRunUnclaimed = errors.New("local CI run record cannot be claimed")

// AbandonedRun reports what one teardown did. Node is empty when no kept folder
// record existed, so only reservations were released.
type AbandonedRun struct {
	RunID        RunID  `json:"runID"`
	Node         string `json:"node,omitempty"`
	Reservations int    `json:"reservations"`
}

// AbandonRun tears down one stale local CI run by its ID and releases its node
// reservations. A run whose owner process still runs is refused: its trial or
// pull request must be cancelled instead. inFlight lists the short heads of the
// runs this engine has in flight.
func (s Store) AbandonRun(ctx context.Context, id RunID, inFlight []string) (AbandonedRun, error) {
	rows, err := s.queryAttachments(ctx, "run_id=?", string(id))
	if err != nil {
		return AbandonedRun{}, err
	}
	r := Runner{DataDir: s.DataDir, Engine: true}
	if len(rows) > 0 {
		if r, err = s.runnerFor(ctx, rows[0].Node); err != nil {
			return AbandonedRun{RunID: id}, err
		}
	}
	return r.AbandonRun(ctx, s, id, inFlight)
}

// AbandonRun tears down the run's kept folder on the runner's node, when a record
// exists, and then releases the run's admitted reservations. The reservations
// are listed before the teardown, so the count covers the checks it releases too.
// A run in flight on this engine is refused before anything is released. An
// admitted row of such a run stores no owner process, so the owner check
// cannot see it.
func (r Runner) AbandonRun(ctx context.Context, store Store, id RunID, inFlight []string) (AbandonedRun, error) {
	out := AbandonedRun{RunID: id}
	for _, head := range inFlight {
		if strings.HasPrefix(string(id), head) {
			return out, fmt.Errorf("%w: run %s is in flight on this engine, so cancel its trial or pull request instead", ErrRunLive, id)
		}
	}
	rows, err := store.queryAttachments(ctx, "run_id=?", string(id))
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		if ownerAlive(row.ownerPID, row.ownerStart) {
			return out, fmt.Errorf("%w: run %s on %s, so cancel its trial or pull request instead", ErrRunLive, id, row.Node)
		}
		if !row.orphaned() {
			return out, fmt.Errorf("%w: run %s on %s has a record past the attachment TTL", ErrRunUnclaimed, id, row.Node)
		}
	}
	queue := r.queue()
	ids, err := queue.RunReservations(ctx, string(id))
	if err != nil {
		return out, err
	}
	if len(rows) == 0 && len(ids) == 0 {
		return out, fmt.Errorf("local CI run %s: %w", id, os.ErrNotExist)
	}
	for _, row := range rows {
		orphan := OrphanedAttachment{Repo: row.Repo, SHA: row.SHA, Node: row.Node, RunID: row.RunID, Source: row.Source}
		torn, err := r.abandonClaimed(ctx, store, orphan)
		if err != nil {
			return out, err
		}
		if !torn {
			return out, fmt.Errorf("%w: run %s on %s was claimed by another process; no reservation was released", ErrRunUnclaimed, id, row.Node)
		}
		out.Node = row.Node
	}
	if err := queue.ReleaseReservations(ctx, ids); err != nil {
		return out, err
	}
	out.Reservations = len(ids)
	return out, nil
}
