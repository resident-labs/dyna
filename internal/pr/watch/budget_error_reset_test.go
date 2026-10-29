package watch

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	prstate "dyna-agent/internal/pr/state"
)

func TestBudgetWriteErrorPreservesFailureStateWhileReconcilingPassedCI(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	r := e.record()
	snap.Runs[0].WorkflowID = 1
	second := snap.Runs[0]
	second.ID, second.WorkflowID, second.Name = 82, 2, "second CI"
	snap.Runs = append(snap.Runs, second)
	e.f.Runs = snap.Runs
	e.f.Raw["repos/owner/repo/actions/runs/82/attempts/1/jobs?per_page=100"] = e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"]
	e.f.Raw["repos/owner/repo/actions/runs/82/artifacts?per_page=100"] = e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"]
	e.write()
	const pause = "CI wakes paused after 3 consecutive failures"
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.CI, live.CIKey = "failed", "budget:"+r.Head+":81/1"
		live.CIReason = "over CI time budget (finished)"
		live.Failures, live.CIPaused, live.PauseReason = 3, true, pause
		live.CIBudgetHead = r.Head
		live.CIBudgetRuns = map[string]string{"81/1": "over CI time budget (finished)", "82/1": "warned"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(e.s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER refuse_second_budget_reset BEFORE INSERT ON pr_rows
WHEN json_extract(NEW.body, '$.record.ciBudgetRuns."82/1"') = 'within budget'
BEGIN SELECT RAISE(ABORT, 'second budget reset refused'); END`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP TRIGGER IF EXISTS refuse_second_budget_reset"); err != nil {
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
	ci, key, _, _ := snap.CIStatus()
	if ci != "passed" {
		t.Fatal("fixture needs passing remote CI after the first budget clears")
	}
	for range 2 {
		st, err := prstate.Read(e.s.Store.Root)
		if err != nil {
			t.Fatal(err)
		}
		gh := e.s.github(r.RoomID)
		gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now}
		err = e.s.runBudgets(t.Context(), pollResult{key: r.Key, gh: gh, snap: snap}, map[string]bool{})
		if err == nil || !strings.Contains(err.Error(), "second budget reset refused") {
			t.Fatalf("later run did not retain its real write error: %v", err)
		}
		got := e.record()
		if string(got.CI) != ci || got.CIKey != key || got.CIReason != "" || got.CIBudgetRuns["81/1"] != "within budget" ||
			got.CIBudgetRuns["82/1"] != "warned" || got.Failures != 3 || !got.CIPaused || got.PauseReason != pause || reprocessingReads != 1 {
			t.Fatalf("failed budget phase lost its committed result or failure pause: CI=%s, key=%s, budgets=%v, failures=%d, paused=%v, reason=%s, reprocessing reads=%d",
				got.CI, got.CIKey, got.CIBudgetRuns, got.Failures, got.CIPaused, got.PauseReason, reprocessingReads)
		}
	}
	if _, err := db.Exec("DROP TRIGGER refuse_second_budget_reset"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		runBudgetFixture(t, e, snap)
		got := e.record()
		if string(got.CI) != ci || got.CIKey != key || got.CIBudgetRuns["81/1"] != "within budget" || got.CIBudgetRuns["82/1"] != "within budget" ||
			got.Failures != 0 || got.CIPaused || got.PauseReason != "" || reprocessingReads != 2 {
			t.Fatalf("successful budget phase did not reset once: CI=%s, key=%s, budgets=%v, failures=%d, paused=%v, reprocessing reads=%d",
				got.CI, got.CIKey, got.CIBudgetRuns, got.Failures, got.CIPaused, reprocessingReads)
		}
	}
}
