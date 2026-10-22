package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func pauseLedgerEnv(t *testing.T) (*Store, Record, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec, &now
}

func TestSessionPauseLedgerPreservesActivityAndResumeGeneration(t *testing.T) {
	s, rec, now := pauseLedgerEnv(t)
	other, err := s.Create(CreateInput{Name: "beta", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	paused, err := s.SetSessionsPaused(context.Background(), rec.ID, true)
	if err != nil || !paused.Paused || paused.Revision != 1 {
		t.Fatalf("pause: %+v, %v", paused, err)
	}
	*now = now.Add(time.Minute)
	repeated, err := s.SetSessionsPaused(context.Background(), rec.Name, true)
	if err != nil || repeated != paused {
		t.Fatalf("repeat pause changed transition: %+v, %v", repeated, err)
	}
	resumed, err := s.SetSessionsPaused(context.Background(), rec.ID, false)
	if err != nil || resumed.Paused || !resumed.ResumePending || resumed.Revision != 2 {
		t.Fatalf("resume: %+v, %v", resumed, err)
	}
	if err = s.CompleteSessionResume(context.Background(), rec.ID, resumed.Revision); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	newest, err := s.SetSessionsPaused(context.Background(), rec.ID, true)
	if err != nil || newest.Revision != 3 {
		t.Fatalf("new pause: %+v, %v", newest, err)
	}
	if err = s.CompleteSessionResume(context.Background(), rec.ID, resumed.Revision); err == nil {
		t.Fatal("stale resume changed newer pause")
	}
	current, err := s.SessionPause(rec.ID)
	if err != nil || current != newest {
		t.Fatalf("stale resume mutated state: %+v, %v", current, err)
	}
	fresh, err := s.Resolve(rec.ID)
	if err != nil || !fresh.LastActivity.Equal(rec.LastActivity) {
		t.Fatalf("pause changed room activity: %+v, %v", fresh, err)
	}
	unaffected, err := s.Paused(other.ID)
	if err != nil || unaffected {
		t.Fatalf("pause escaped room: %v, %v", unaffected, err)
	}
}

func TestSessionPauseSpansClipHistoryAndKeepOngoingSpan(t *testing.T) {
	s, rec, now := pauseLedgerEnv(t)
	start := *now
	for _, step := range []struct {
		after  time.Duration
		paused bool
	}{{5 * time.Minute, true}, {15 * time.Minute, false}, {25 * time.Minute, true}} {
		*now = start.Add(step.after)
		if _, err := s.SetSessionsPaused(context.Background(), rec.ID, step.paused); err != nil {
			t.Fatal(err)
		}
	}
	*now = start.Add(35 * time.Minute)
	reopened := &Store{Root: s.Root, Now: s.Now}
	spans, err := reopened.PauseSpans(rec.ID, start.Add(10*time.Minute))
	if err != nil || len(spans) != 2 || !spans[0].From.Equal(start.Add(10*time.Minute)) || !spans[0].To.Equal(start.Add(15*time.Minute)) || !spans[1].From.Equal(start.Add(25*time.Minute)) || !spans[1].To.IsZero() {
		t.Fatalf("clipped spans: %+v, %v", spans, err)
	}
	spans, err = reopened.PauseSpans(rec.ID, start.Add(30*time.Minute))
	if err != nil || len(spans) != 1 || !spans[0].From.Equal(start.Add(30*time.Minute)) || !spans[0].To.IsZero() {
		t.Fatalf("ongoing span: %+v, %v", spans, err)
	}
	events, err := reopened.SessionPauseEvents(rec.ID, 2)
	if err != nil || len(events) != 1 || events[0].Revision != 3 || events[0].Kind != "room.paused" {
		t.Fatalf("event cursor: %+v, %v", events, err)
	}
}

func TestSessionPauseLedgerRepairsLastEventWithoutRepeatingTransition(t *testing.T) {
	s, rec, now := pauseLedgerEnv(t)
	paused, err := s.SetSessionsPaused(context.Background(), rec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	resumed, err := s.SetSessionsPaused(context.Background(), rec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.dir(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-pause.jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The metadata commit survived, but the last ledger append did not.
	if err = os.WriteFile(path, raw[:strings.IndexByte(string(raw), '\n')+1], 0600); err != nil {
		t.Fatal(err)
	}
	reopened := &Store{Root: s.Root, Now: s.Now}
	events, err := reopened.SessionPauseEvents(rec.ID, 0)
	if err != nil || len(events) != 2 || events[0].Revision != paused.Revision || events[1].Revision != resumed.Revision {
		t.Fatalf("read recovery: %+v, %v", events, err)
	}
	*now = now.Add(time.Minute)
	repeated, err := reopened.SetSessionsPaused(context.Background(), rec.ID, false)
	if err != nil || repeated != resumed {
		t.Fatalf("repair repeated transition: %+v, %v", repeated, err)
	}
	events, err = reopened.readPauseEvents(rec.ID)
	if err != nil || len(events) != 2 || events[1].Kind != "room.resumed" {
		t.Fatalf("durable repaired events: %+v, %v", events, err)
	}
	if err = os.WriteFile(path, []byte("{broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.PauseSpans(rec.ID, time.Time{}); err == nil {
		t.Fatal("corrupt history was treated as no pause time")
	}
}

func TestSessionPauseCancelledLockWaitDoesNotMutate(t *testing.T) {
	s, rec, _ := pauseLedgerEnv(t)
	dir, err := s.dir(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "room.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	entered := make(chan struct{})
	go func() { close(entered); _, err := s.SetSessionsPaused(ctx, rec.ID, true); done <- err }()
	<-entered
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled pause succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled pause remained blocked on room lock")
	}
	current, err := s.SessionPause(rec.ID)
	if err != nil || current.Paused || current.Revision != 0 {
		t.Fatalf("cancelled pause mutated state: %+v, %v", current, err)
	}
}
