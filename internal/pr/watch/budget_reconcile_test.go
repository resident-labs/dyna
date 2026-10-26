package watch

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	prstate "dyna-agent/internal/pr/state"
	appstate "dyna-agent/internal/state"
)

func overBudgetFixture(t *testing.T) (*testEnv, Snapshot) {
	t.Helper()
	e, snap := completedBudgetFixture(t)
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	r := e.record()
	policy := EffectivePolicy(st, r)
	policy.CIWarnAfter, policy.CIFailAfter = "2s", "3s"
	if err := e.s.SetRoomChangePolicy(r.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key].CI, st.Records[r.Key].CIKey = "pending", ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.write()
	return e, snap
}

func TestBudgetCommitIsReconciledBeforeLaterRunError(t *testing.T) {
	e, snap := overBudgetFixture(t)
	r := e.record()
	snap.Runs[0].WorkflowID = 1
	second := snap.Runs[0]
	second.ID, second.WorkflowID, second.Name = 82, 2, "second CI"
	snap.Runs = append(snap.Runs, second)
	e.f.Runs = snap.Runs
	firstJobs := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	e.f.Raw["repos/owner/repo/actions/runs/82/attempts/1/jobs?per_page=100"] = e.f.Raw[firstJobs]
	e.write()
	db, err := database.Open(filepath.Dir(e.s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER refuse_second_budget BEFORE INSERT ON pr_rows
WHEN json_extract(NEW.body, '$.record.ciBudgetRuns."82/1"') IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'second budget refused'); END`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP TRIGGER refuse_second_budget"); err != nil {
			t.Error(err)
		}
	})
	original := readStore
	reprocessingReads := 0
	readStore = func(root string) (State, error) {
		reprocessingReads++
		return original(root)
	}
	t.Cleanup(func() { readStore = original })
	for range 2 {
		st, err := prstate.Read(e.s.Store.Root)
		if err != nil {
			t.Fatal(err)
		}
		gh := e.s.github(r.RoomID)
		gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now}
		err = e.s.runBudgets(t.Context(), pollResult{key: r.Key, gh: gh, snap: snap}, map[string]bool{})
		if err == nil || !strings.Contains(err.Error(), "second budget refused") {
			t.Fatalf("later run did not return its real write error: %v", err)
		}
	}
	got := e.record()
	if got.CI != "failed" || got.CIKey != "budget:"+snap.Pull.Head.SHA+":81/1" || got.Failures != 1 ||
		!strings.HasPrefix(got.CIBudgetRuns["81/1"], "over CI time budget") || got.CIBudgetRuns["82/1"] != "" || reprocessingReads != 1 {
		t.Fatalf("committed run was not reconciled exactly once: CI=%s, key=%s, failures=%d, budgets=%v, reprocessing reads=%d",
			got.CI, got.CIKey, got.Failures, got.CIBudgetRuns, reprocessingReads)
	}
}

func TestBudgetCommitIsReconciledAfterServiceReopen(t *testing.T) {
	e, snap := overBudgetFixture(t)
	r := e.record()
	unlock, owned, err := e.s.tryPublicationGate(r.Key)
	if err != nil || !owned {
		t.Fatalf("hold existing publication gate: owned=%v, error=%v", owned, err)
	}
	release := sync.OnceFunc(unlock)
	defer release()
	runBudgetFixture(t, e, snap)
	committed := e.record()
	if committed.CI != "pending" || !strings.HasPrefix(committed.CIBudgetRuns["81/1"], "over CI time budget") {
		t.Fatalf("fixture did not stop after budget commit and before processing: CI=%s, budgets=%v", committed.CI, committed.CIBudgetRuns)
	}
	release()
	if err := database.Close(filepath.Dir(e.s.Store.Root)); err != nil {
		t.Fatal(err)
	}
	// Reopen the database and service without an in-memory change flag.
	reopened := *e.s
	reopened.Store = &Store{Root: e.s.Store.Root}
	e.s = &reopened
	db, err := database.Open(filepath.Dir(e.s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	writer := appstate.For(db)
	beforeWrites := writer.Health().Writes
	var published atomic.Int64
	writer.SetPublish(func(ev hub.Event) {
		if ev.Topic == prstate.Topic {
			published.Add(1)
		}
	})
	original := readStore
	reprocessingReads := 0
	readStore = func(root string) (State, error) {
		reprocessingReads++
		return original(root)
	}
	t.Cleanup(func() { readStore = original })
	for range 2 {
		runBudgetFixture(t, e, snap)
	}
	got := e.record()
	if got.CI != "failed" || got.CIKey != "budget:"+snap.Pull.Head.SHA+":81/1" || got.Failures != 1 ||
		reprocessingReads != 1 || writer.Health().Writes-beforeWrites != 1 || published.Load() != 1 {
		t.Fatalf("reopened service did not reconcile once: CI=%s, key=%s, failures=%d, reprocessing reads=%d, writes=%d, publications=%d",
			got.CI, got.CIKey, got.Failures, reprocessingReads, writer.Health().Writes-beforeWrites, published.Load())
	}
}
