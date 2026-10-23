package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestResumeTurnReportsWaitsForTurnLockRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	service, rec, session, turnLock := pendingEngineCompletionFixture(t, ctx)
	workers := subagent.NewWorkers(ctx, t.Logf)
	service.Sessions.Workers = workers
	configureTurnReports(ctx, service)
	defer func() {
		cancel()
		_ = turnLock.Close()
		if left := workers.Wait(30 * time.Second); len(left) != 0 {
			t.Errorf("completion workers did not stop: %v", left)
		}
	}()

	// Startup recovery must leave a completed row alone while its runner
	// still owns the turn lock. Wait joins any wrongly scheduled worker.
	if err := resumeTurnReports(ctx, service, ""); err != nil {
		t.Fatal(err)
	}
	if left := workers.Wait(30 * time.Second); len(left) != 0 {
		t.Fatalf("startup scheduled a report before turn lock release: %v", left)
	}
	requireEngineCompletionPending(t, ctx, service, session.ID)
	if posts := engineRecoveryCompletions(t, service, rec.ID, session); len(posts) != 0 {
		t.Fatalf("reported before turn lock release: %+v", posts)
	}

	if err := turnLock.Close(); err != nil {
		t.Fatal(err)
	}
	// The runner's exit wake resumes the same durable row.
	if err := resumeTurnReports(ctx, service, session.ID); err != nil {
		t.Fatal(err)
	}
	if left := workers.Wait(30 * time.Second); len(left) != 0 {
		t.Fatalf("completion remained pending after turn lock release: %v", left)
	}
	posts := engineRecoveryCompletions(t, service, rec.ID, session)
	if len(posts) != 1 || posts[0].ID == "" {
		t.Fatalf("exit recovery stored %+v; want one completion", posts)
	}
	postID := posts[0].ID
	turn, err := service.Sessions.Store.ReadTurn(session.ID, 1)
	if err != nil || !turn.Reported || turn.ReportPending {
		t.Fatalf("completion was not finalized: %+v, %v", turn, err)
	}
	if pending, err := service.Sessions.Store.PendingReports(ctx, session.ID); err != nil || len(pending) != 0 {
		t.Fatalf("completed row remains in the pending index: %+v, %v", pending, err)
	}
	if err := resumeTurnReports(ctx, service, ""); err != nil {
		t.Fatal(err)
	}
	if left := workers.Wait(30 * time.Second); len(left) != 0 {
		t.Fatalf("repeated recovery left workers running: %v", left)
	}
	if posts := engineRecoveryCompletions(t, service, rec.ID, session); len(posts) != 1 || posts[0].ID != postID {
		t.Fatalf("repeated recovery duplicated completion %s: %+v", postID, posts)
	}
}

func TestTurnReportWorkerCancellationKeepsPendingRow(t *testing.T) {
	deadline, stopDeadline := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopDeadline()
	service, rec, session, turnLock := pendingEngineCompletionFixture(t, deadline)
	if err := turnLock.Close(); err != nil {
		t.Fatal(err)
	}
	release, err := service.Sessions.Store.LockReport(deadline, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan struct{}, 1)
	cancellable, cancel := context.WithCancel(deadline)
	ctx := store.WithFlockContentionHook(cancellable, func(path string) {
		if path == filepath.Join(service.Sessions.Store.Root, session.ID, "report.lock") {
			select {
			case waiting <- struct{}{}:
			default:
			}
		}
	})
	workers := subagent.NewWorkers(ctx, t.Logf)
	service.Sessions.Workers = workers
	configureTurnReports(ctx, service)
	defer func() {
		cancel()
		release()
		if left := workers.Wait(30 * time.Second); len(left) != 0 {
			t.Errorf("completion workers did not stop: %v", left)
		}
	}()
	if err := resumeTurnReports(ctx, service, session.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waiting:
	case <-deadline.Done():
		t.Fatal("completion worker did not reach the held report lock")
	}
	cancel()
	// Keep report.lock held until Wait proves that engine cancellation ended
	// the worker. A background-context worker would remain blocked here.
	if left := workers.Wait(30 * time.Second); len(left) != 0 {
		t.Fatalf("engine cancellation left completion workers blocked: %v", left)
	}
	requireEngineCompletionPending(t, deadline, service, session.ID)
	if posts := engineRecoveryCompletions(t, service, rec.ID, session); len(posts) != 0 {
		t.Fatalf("cancelled worker stored a completion: %+v", posts)
	}
}

func pendingEngineCompletionFixture(t *testing.T, ctx context.Context) (*notify.Service, room.Record, subagent.Info, *os.File) {
	t.Helper()
	storetest.Isolate(t)
	pending := make(chan pendingSessionTurn, 1)
	manager, rec := sessionWorkerFixture(t, ctx, pending)
	t.Cleanup(func() {
		for _, root := range []string{manager.Store.Root, manager.Rooms.Root} {
			if err := database.Close(filepath.Dir(root)); err != nil {
				t.Error(err)
			}
		}
	})
	session, turn, err := manager.Start(ctx, subagent.StartOptions{
		Goal: "Test durable completion recovery", Profile: "mock", Name: "completion",
		Room: rec.Name, Dir: t.TempDir(), Task: "RESPOND: ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	var held pendingSessionTurn
	select {
	case held = <-pending:
	case <-ctx.Done():
		t.Fatal("fixture did not retain the turn lock")
	}
	t.Cleanup(func() { _ = held.lock.Close() })
	if _, err := manager.Store.UpdateTurn(session.ID, turn.N, func(row *subagent.Turn) {
		row.Status, row.Ended = subagent.TurnOK, time.Now()
		row.Result, row.ReportPending = "ready", true
	}); err != nil {
		t.Fatal(err)
	}
	return notify.New(manager.Rooms, manager), rec, session, held.lock
}

func requireEngineCompletionPending(t *testing.T, ctx context.Context, service *notify.Service, sessionID string) {
	t.Helper()
	turn, err := service.Sessions.Store.ReadTurn(sessionID, 1)
	if err != nil || !turn.Done() || turn.Reported || !turn.ReportPending {
		t.Fatalf("completion intent was consumed: %+v, %v", turn, err)
	}
	pending, err := service.Sessions.Store.PendingReports(ctx, sessionID)
	if err != nil || len(pending) != 1 || pending[0].SessionID != sessionID || pending[0].Turn != 1 {
		t.Fatalf("missing durable pending row: %+v, %v", pending, err)
	}
}

func engineRecoveryCompletions(t *testing.T, service *notify.Service, roomID string, session subagent.Info) []room.Message {
	t.Helper()
	posts, err := service.Rooms.Messages(roomID)
	if err != nil {
		t.Fatal(err)
	}
	var completions []room.Message
	for _, post := range posts {
		if ev, ok := post.TurnResult(); ok && ev.SessionID == session.ID && ev.Turn == 1 && ev.State == room.TurnCompleted {
			completions = append(completions, post)
		}
	}
	return completions
}
