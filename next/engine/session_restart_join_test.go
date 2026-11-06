package engine

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type restartJoinBoxes struct {
	fakeBoxes
	target      contract.BoxID
	entered     chan context.Context
	release     chan struct{}
	oldFinished atomic.Bool
	deleted     atomic.Bool
}

func (b *restartJoinBoxes) Remove(ctx context.Context, id contract.BoxID) error {
	if id != b.target {
		return nil
	}
	b.entered <- ctx
	<-b.release // model host work that finishes after local cancellation
	b.oldFinished.Store(true)
	return errors.New("old host reply")
}
func (b *restartJoinBoxes) RemoveFailedBox(_ context.Context, old box.Box) error {
	if !b.oldFinished.Load() {
		return errors.New("destructive restart overtook old lifecycle work")
	}
	b.deleted.Store(true)
	return nil
}

// The context gates only this caller's first SQL lookup, before authorization
// finishes. This exercises a genuinely delayed API entry without a service hook.
type restartLookupContext struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
}

func (c *restartLookupContext) Done() <-chan struct{} {
	// The first check holds database/sql's pool mutex. Gate the driver query
	// check instead, after that mutex is released, so other callers can progress.
	if c.calls.Add(1) == 2 {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	return c.Context.Done()
}

type restartCountBoxes struct {
	fakeBoxes
	calls atomic.Int32
}

func (b *restartCountBoxes) RemoveFailedBox(context.Context, box.Box) error {
	b.calls.Add(1)
	return nil
}

func TestSessionRestartDelayedAuthorizationCannotDiscardNewFailedWork(t *testing.T) {
	s, _, worker, ctx := setup(t)
	adapter := &restartCountBoxes{}
	s.boxes = adapter
	resetLifecycle(s)
	write := func(query string) {
		t.Helper()
		if err := s.store.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, worker); return err }); err != nil {
			t.Fatal(err)
		}
	}
	write(`UPDATE boxes SET state='failed',reason='old failure' WHERE session=?`)
	delayed := &restartLookupContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	result := make(chan *contract.Error, 1)
	finished := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(delayed.release) }); <-finished })
	go func() {
		_, problem := s.SessionRestart(delayed, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionRestartRequest{Session: worker})
		result <- problem
		close(finished)
	}()
	select {
	case <-delayed.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("delayed API did not enter its authorized lookup")
	}
	if _, problem := s.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionRestartRequest{Session: worker}); problem != nil {
		t.Fatal(problem)
	}
	// The first restart has completed, and its new incarnation has since failed
	// after creating work. The older API entry still must not discard this work.
	write(`UPDATE boxes SET state='failed',image='new-work-image',reason='new failure',updated_at=updated_at+1 WHERE session=?`)
	release.Do(func() { close(delayed.release) })
	if problem := <-result; problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("stale API caller deleted newer failed incarnation: %v", problem)
	}
	if got := adapter.calls.Load(); got != 1 {
		t.Fatalf("stale API caller reached destructive cleanup: calls=%d", got)
	}
	var state, image string
	if err := s.store.Read().QueryRow(`SELECT state,image FROM boxes WHERE session=?`, worker).Scan(&state, &image); err != nil || state != "failed" || image != "new-work-image" {
		t.Fatalf("new failed work changed: %s %q %v", state, image, err)
	}
}

func TestSessionRestartJoinsLateLifecycleCompletionBeforeDeletion(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-unchanged", true: "failed-snapshot-changed"}[stale], func(t *testing.T) {
			s, _, worker, ctx := setup(t)
			session, err := s.store.Session(ctx, worker)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &restartJoinBoxes{target: session.Box, entered: make(chan context.Context, 1), release: make(chan struct{})}
			s.boxes = adapter
			resetLifecycle(s)
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(adapter.release) }); s.reconciler.Wait() })
			write := func(query string) {
				t.Helper()
				if err := s.store.Write(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query, worker); return err }); err != nil {
					t.Fatal(err)
				}
			}
			write(`UPDATE boxes SET state='creating',image='',want='run',due_at=0 WHERE session=?`)
			if err := s.reconciler.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			var active context.Context
			select {
			case active = <-adapter.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("old lifecycle step not started")
			}
			write(`UPDATE boxes SET state='failed',reason='old failed' WHERE session=?`)
			result := make(chan *contract.Error, 1)
			go func() {
				_, problem := s.SessionRestart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionRestartRequest{Session: worker})
				result <- problem
			}()
			<-active.Done() // restart has claimed the box and is joining this old owner
			select {
			case problem := <-result:
				t.Fatalf("API returned before join: %v", problem)
			case <-time.After(100 * time.Millisecond):
			}
			if adapter.deleted.Load() {
				t.Fatal("deleted before old host work finished")
			}
			if stale {
				write(`UPDATE boxes SET updated_at=updated_at+1,image='newer-image' WHERE session=?`)
			}
			release.Do(func() { close(adapter.release) })
			problem := <-result
			if stale {
				if problem == nil || problem.Code != contract.ErrConflict || adapter.deleted.Load() {
					t.Fatalf("stale failed snapshot deleted newer work: %v deleted=%v", problem, adapter.deleted.Load())
				}
			} else if problem != nil || !adapter.deleted.Load() {
				t.Fatalf("joined restart: %v deleted=%v", problem, adapter.deleted.Load())
			}
			s.reconciler.Wait()
			b, err := s.store.Box(ctx, session.Box)
			if err != nil {
				t.Fatal(err)
			}
			if !stale && b.State != contract.BoxPending {
				t.Fatalf("late completion overwrote reset: %+v", b)
			}
			if stale && (b.State != contract.BoxFailed || b.Image != "newer-image") {
				t.Fatalf("stale completion changed metadata: %+v", b)
			}
		})
	}
}
