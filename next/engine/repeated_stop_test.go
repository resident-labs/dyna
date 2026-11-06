package engine

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type repeatedStopBoxes struct {
	fakeBoxes
	target  contract.BoxID
	entered chan context.Context
	release chan struct{}
	calls   atomic.Int32
}

func (b *repeatedStopBoxes) Remove(ctx context.Context, id contract.BoxID) error {
	if id != b.target {
		return nil
	}
	b.calls.Add(1)
	if b.entered != nil {
		b.entered <- ctx
		<-b.release
	}
	return errors.New("host unavailable")
}
func (b *repeatedStopBoxes) Stop(ctx context.Context, id contract.BoxID) error {
	return b.Remove(ctx, id)
}

// Exercise duplicate owner Stop through the actual service after cleanup has
// really failed and recorded its deadline, rather than injecting a future due_at.
func TestRepeatedSessionStopPreservesCleanupScheduleAfterReconstruction(t *testing.T) {
	for _, state := range []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting} {
		t.Run(string(state), func(t *testing.T) {
			s, _, worker, ctx := setup(t)
			session, err := s.store.Session(ctx, worker)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &repeatedStopBoxes{target: session.Box}
			s.boxes = adapter
			resetLifecycle(s)
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state=?,image='',want='run',due_at=0,create_retry_host='first-host' WHERE session=?", state, worker)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stop := func() {
				t.Helper()
				answer, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker})
				if problem != nil || !answer.Stopping {
					t.Fatalf("Stop: %+v %v", answer, problem)
				}
			}
			stop()
			passLifecycle(t, s, ctx)
			var beforeDue int64
			var beforeFailures int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT due_at,failures FROM boxes WHERE session=?", worker).Scan(&beforeDue, &beforeFailures); err != nil {
				t.Fatal(err)
			}
			if beforeDue <= s.now().UnixMilli() || beforeFailures != 1 {
				t.Fatalf("initial cleanup did not schedule retry: due=%d failures=%d", beforeDue, beforeFailures)
			}
			for range 3 {
				stop()
			}
			var afterDue int64
			var failures int
			var want, retryHost string
			if err := s.store.Read().QueryRowContext(ctx, "SELECT due_at,failures,want,create_retry_host FROM boxes WHERE session=?", worker).Scan(&afterDue, &failures, &want, &retryHost); err != nil {
				t.Fatal(err)
			}
			if afterDue != beforeDue || failures != beforeFailures || want != "stop" || retryHost != "first-host" {
				t.Errorf("repeated Stop changed existing cleanup schedule or retry intent: before=(due %d failures %d), after=(due %d failures %d want %s retryHost %s)", beforeDue, beforeFailures, afterDue, failures, want, retryHost)
			}
			// Reconstruct the reconciler: the saved deadline must be sufficient without
			// process-local scheduling state to suppress another call before it is due.
			resetLifecycle(s)
			passLifecycle(t, s, ctx)
			if got := adapter.calls.Load(); got != 1 {
				t.Errorf("same-clock Stop replay triggered host cleanup again: calls=%d", got)
			}
		})
	}
}

// An unrelated Pass and an identical API Stop both keep want=stop. Neither is a
// newer owner intent that should cancel this already-owned cleanup operation.
func TestSessionStopDoesNotCancelAlreadyOwnedCleanup(t *testing.T) {
	for _, action := range []string{"unrelated-pass", "repeated-api-stop"} {
		t.Run(action, func(t *testing.T) {
			s, _, worker, ctx := setup(t)
			session, err := s.store.Session(ctx, worker)
			if err != nil {
				t.Fatal(err)
			}
			adapter := &repeatedStopBoxes{target: session.Box, entered: make(chan context.Context, 1), release: make(chan struct{})}
			s.boxes = adapter
			resetLifecycle(s)
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(adapter.release) }); s.reconciler.Wait() })
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state='creating',image='',want='run',due_at=0 WHERE session=?", worker)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			stop := func() {
				t.Helper()
				answer, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker})
				if problem != nil || !answer.Stopping {
					t.Fatalf("Stop: %+v %v", answer, problem)
				}
			}
			stop()
			if err := s.reconciler.Pass(ctx); err != nil {
				t.Fatal(err)
			}
			var cleanup context.Context
			select {
			case cleanup = <-adapter.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup did not start")
			}
			for range 3 {
				if action == "unrelated-pass" {
					if err := s.reconciler.Pass(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					stop()
				}
			}
			if err := cleanup.Err(); err != nil {
				t.Errorf("same-intent %s canceled owned cleanup: %v", action, err)
			}
			release.Do(func() { close(adapter.release) })
			s.reconciler.Wait()
			var due int64
			var failures int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT due_at,failures FROM boxes WHERE session=?", worker).Scan(&due, &failures); err != nil {
				t.Fatal(err)
			}
			if due <= s.now().UnixMilli() || failures != 1 {
				t.Errorf("same-intent %s lost cleanup retry metadata: due=%d now=%d failures=%d", action, due, s.now().UnixMilli(), failures)
			}
		})
	}
}

func TestFirstSessionStopStillCancelsPendingStartPromptly(t *testing.T) {
	s, _, worker, ctx := setup(t)
	session, err := s.store.Session(ctx, worker)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &repeatedStopBoxes{target: session.Box, entered: make(chan context.Context, 1), release: make(chan struct{})}
	s.boxes = adapter
	resetLifecycle(s)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(adapter.release) }); s.reconciler.Wait() })
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='creating',image='',want='run',due_at=0 WHERE session=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconciler.Pass(ctx); err != nil {
		t.Fatal(err)
	}
	var pending context.Context
	select {
	case pending = <-adapter.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pending start did not enter runtime")
	}
	answer, problem := s.SessionStop(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStopRequest{Session: worker})
	if problem != nil || !answer.Stopping {
		t.Fatalf("first Stop: %+v %v", answer, problem)
	}
	if err := pending.Err(); err != context.Canceled {
		t.Fatalf("first Stop did not promptly cancel pending start: %v", err)
	}
	release.Do(func() { close(adapter.release) })
	s.reconciler.Wait()
}
