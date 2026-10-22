package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

type finishLockSpawner struct{}

func (finishLockSpawner) Spawn(_ *subagent.Manager, _ string, _ int, lock *os.File) error {
	_ = lock.Close()
	return errors.New("test reviewer reached session startup")
}

func TestFinishOverlapsReviewerStartupWithoutDeadlock(t *testing.T) {
	storetest.ClearStamp(t)
	e := newEnv(t)
	r := e.track()
	m := subagent.NewManager()
	m.Store = &subagent.Store{Root: filepath.Join(filepath.Dir(e.s.Store.Root), "sessions")}
	m.Profiles = func() (*profile.Store, error) {
		return &profile.Store{Profiles: []profile.Profile{{Name: "mock", Harness: profile.HarnessMock}}}, nil
	}
	m.Spawner = finishLockSpawner{}
	rec, err := m.RoomStore().Create(room.CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err = e.s.Store.update(func(st *State) error { st.Reviewers[r.RoomID] = Reviewer{Profile: "mock"}; return nil }); err != nil {
		t.Fatal(err)
	}
	entered, proceed, processed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	e.s.LaunchReviewer = func(_ context.Context, _ Record, _, _, _ string) (string, error) {
		close(entered)
		<-proceed
		_, _, err := m.Start(context.Background(), subagent.StartOptions{Goal: "Finish the test task", Profile: "mock", Room: rec.ID, Dir: e.author.Dir, Task: "review"})
		return "", err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	go func() {
		err := e.s.process(ctx, r.Key, Snapshot{Pull: e.f.Pull}, "passed", "pass", nil, nil, false)
		if err == nil {
			err = e.s.dispatchAll(context.Background())
		}
		processed <- err
	}()
	select {
	case <-entered: // Reviewer startup is paused after reserving its review.
	case <-ctx.Done():
		close(proceed)
		t.Fatal("reviewer did not reach startup")
	}
	// Prove the lock contract directly. A held publication lock is the
	// deadlock regression; slow Finish metadata writes are not evidence of it.
	unlock, owned, err := store.TryLock(state.LockPath(e.s.Store.Root))
	if err != nil || !owned {
		close(proceed)
		<-processed
		t.Fatalf("reviewer startup retained the publication lock: owned=%v error=%v", owned, err)
	}
	unlock()
	target := transferTarget(e)
	_, finishErr := m.Finish(ctx, target.ID)
	close(proceed)
	processErr := <-processed
	if finishErr != nil {
		t.Fatalf("finish deadlocked with reviewer startup: %v", finishErr)
	}
	if processErr != nil {
		t.Fatal(processErr)
	}
	if ctx.Err() != nil {
		t.Fatal("operations only completed after cancellation")
	}
	info, err := m.Store.Info(target.ID)
	if err != nil || info.Finished.IsZero() || info.Status == subagent.StatusClosed {
		t.Fatalf("finish did not make the session dormant: %+v %v", info, err)
	}
}

func TestReviewerStartupRechecksReservationBeforeAssigning(t *testing.T) {
	for _, change := range []string{"head", "untrack", "configuration"} {
		t.Run(change, func(t *testing.T) {
			e := newEnv(t)
			r := e.track()
			if err := e.s.Store.update(func(st *State) error { st.Reviewers[r.RoomID] = Reviewer{Profile: "mock"}; return nil }); err != nil {
				t.Fatal(err)
			}
			e.s.LaunchReviewer = func(_ context.Context, _ Record, _, _, _ string) (string, error) {
				err := e.s.Store.update(func(st *State) error {
					switch change {
					case "head":
						st.Records[r.Key].Head = "new-head"
					case "untrack":
						st.Records[r.Key].Watching = false
					case "configuration":
						st.Reviewers[r.RoomID] = Reviewer{Profile: "judge"}
					}
					return nil
				})
				return "stale-reviewer", err
			}
			if err := e.s.process(context.Background(), r.Key, Snapshot{Pull: e.f.Pull}, "passed", "pass", nil, nil, false); err != nil {
				t.Fatal(err)
			}
			if err := e.s.dispatchAll(context.Background()); err != nil {
				t.Fatal(err)
			}
			if current := e.record(); current.Reviewer != "" {
				t.Fatalf("stale startup replaced a changed reservation: %+v", current)
			}
		})
	}
}
