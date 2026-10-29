package localci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
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

// AbandonedRun reports what one teardown did. Node is empty when no kept folder
// record existed, so only reservations were released.
type AbandonedRun struct {
	RunID        RunID  `json:"runID"`
	Node         string `json:"node,omitempty"`
	Reservations int    `json:"reservations"`
}

// AbandonRun tears down one stale local CI run by its ID and releases its node
// reservations. A run whose owner process still runs is refused: its trial or
// pull request must be cancelled instead.
func (s Store) AbandonRun(ctx context.Context, id RunID) (AbandonedRun, error) {
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
	return r.AbandonRun(ctx, s, id)
}

// AbandonRun tears down the run's kept folder on the runner's node, when a record
// exists, and then releases the run's admitted reservations. The reservations
// are listed before the teardown, so the count covers the checks it releases too.
func (r Runner) AbandonRun(ctx context.Context, store Store, id RunID) (AbandonedRun, error) {
	out := AbandonedRun{RunID: id}
	rows, err := store.queryAttachments(ctx, "run_id=?", string(id))
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		if ownerAlive(row.ownerPID, row.ownerStart) {
			return out, fmt.Errorf("%w: run %s on %s, so cancel its trial or pull request instead", ErrRunLive, id, row.Node)
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
		if err := r.AbandonOrphanedRun(ctx, store, orphan); err != nil {
			return out, err
		}
		out.Node = row.Node
	}
	if err := queue.ReleaseReservations(ctx, ids); err != nil {
		return out, err
	}
	out.Reservations = len(ids)
	return out, nil
}
