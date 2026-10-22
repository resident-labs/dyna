package watch

import (
	"context"
	"errors"
	"testing"
	"time"

	"dyna-agent/internal/store/storetest"
)

// A watched PR keeps the PR watcher due one poll later: the head batch is
// how it sees a push, a review or a CI result.
func TestTickIsDueAPollLaterWhileAPRIsWatched(t *testing.T) {
	e := newEnv(t)
	e.write()
	e.track()
	next, err := e.s.Tick(t.Context())
	if err != nil || !next.Equal(e.clock.Add(pollEvery)) {
		t.Fatalf("Tick = %v, %v; want %v", next, err, e.clock.Add(pollEvery))
	}
}

// A notice that failed to reach its room keeps the pass due one poll later,
// even on a merged PR that is no longer watched.
func TestTickRetriesAPendingNoticeOnAnUnwatchedRecord(t *testing.T) {
	storetest.DataDir(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: &Store{Root: t.TempDir()}, Now: func() time.Time { return now }}
	s.Deliver = func(context.Context, Record, Notice) (string, error) { return "", errors.New("room is busy") }
	if err := s.Store.update(func(st *State) error {
		st.Records = map[string]*Record{"a": {Key: "a", State: "merged", Notices: []Notice{{ID: "n1", Text: "PR merged."}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next, _ := s.Tick(t.Context())
	if !next.Equal(now.Add(pollEvery)) {
		t.Fatalf("Tick next = %v, want %v", next, now.Add(pollEvery))
	}
}

// A running merge trial keeps the merge service due one poll later.
func TestMergeTickIsDueAPollLaterWhileATrialRuns(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	next, err := f.e.s.MergeTick(t.Context())
	if err != nil || !next.Equal(f.e.clock.Add(pollEvery)) {
		t.Fatalf("MergeTick = %v, %v; want %v", next, err, f.e.clock.Add(pollEvery))
	}
}

// An active sync operation keeps SyncTick due one poll later; a finished one
// does not.
func TestSyncTickIsDueOnlyWhileAnOperationIsActive(t *testing.T) {
	e, repo, _, _ := syncEnv(t)
	if started, err := e.s.SyncRefresh(context.Background(), e.author.RoomID, repo); err != nil || !started.Started {
		t.Fatalf("start: %+v %v", started, err)
	}
	next, err := e.s.SyncTick(t.Context())
	if err != nil || !next.Equal(e.clock.Add(pollEvery)) {
		t.Fatalf("SyncTick while active = %v, %v; want %v", next, err, e.clock.Add(pollEvery))
	}
	finishSync(t, e, repo)
	if next, err = e.s.SyncTick(t.Context()); err != nil || !next.IsZero() {
		t.Fatalf("SyncTick after the operation = %v, %v; want zero", next, err)
	}
}

// With an empty store, each pass returns no next time, so the engine sets no
// timer for it.
func TestEmptyPRPassesSetNoTimer(t *testing.T) {
	storetest.DataDir(t)
	s := &Service{Store: &Store{Root: t.TempDir()}}
	for name, pass := range map[string]func(context.Context) (time.Time, error){"Tick": s.Tick, "MergeTick": s.MergeTick, "SyncTick": s.SyncTick} {
		next, err := pass(t.Context())
		if err != nil || !next.IsZero() {
			t.Errorf("%s = %v, %v; want zero and no error", name, next, err)
		}
	}
}
