package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
	prstate "dyna-agent/internal/pr/state"
)

func countSavedCIDecodes(t *testing.T) *atomic.Int64 {
	t.Helper()
	original := unmarshalSavedCI
	count := new(atomic.Int64)
	unmarshalSavedCI = func(body []byte, out any) error {
		count.Add(1)
		return original(body, out)
	}
	t.Cleanup(func() { unmarshalSavedCI = original })
	return count
}

// Place four due ticks before the existing safety slot, without waiting.
func prepareQuietBudgetTicks(t *testing.T, e *testEnv, key string) {
	t.Helper()
	for offset := range 9 {
		first := e.clock.Add(time.Duration(offset+1) * time.Minute)
		record := Record{Key: key, Polling: Polling{FullChecked: first}}
		if !fullPollDue(record, first.Add(time.Minute)) && !fullPollDue(record, first.Add(2*time.Minute)) && !fullPollDue(record, first.Add(3*time.Minute)) {
			e.clock = first.Add(-time.Minute)
			return
		}
	}
	t.Fatal("could not place quiet recovery before the safety poll")
}

func TestTickReconcilesCommittedBudgetBeforeQuietSafetyPoll(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	e, _ := overBudgetFixture(t)
	e.authorFeedOnly()
	r := e.record()
	prepareQuietBudgetTicks(t, e, r.Key)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.write()
	original := readBudgetSnapshot
	var normal Record
	var release func()
	readBudgetSnapshot = func(root string) (prstate.Snapshot, error) {
		snapshot, err := original(root)
		if err == nil && release == nil {
			var ok bool
			normal, ok = snapshot.Record(r.Key)
			if !ok || normal.CI != "passed" || normal.CIKey == "" || normal.CIBudgetRuns["81/1"] != "" {
				return snapshot, fmt.Errorf("normal CI was not processed before budgets: CI=%s, key=%s, budgets=%v", normal.CI, normal.CIKey, normal.CIBudgetRuns)
			}
			unlock, owned, gateErr := e.s.tryPublicationGate(r.Key)
			if gateErr != nil || !owned {
				return snapshot, fmt.Errorf("hold publication after normal processing: owned=%v, error=%v", owned, gateErr)
			}
			release = sync.OnceFunc(unlock)
		}
		return snapshot, err
	}
	t.Cleanup(func() {
		readBudgetSnapshot = original
		if release != nil {
			release()
		}
	})
	// The real Tick processes successful remote CI, then commits its budget.
	// The held gate leaves only reconciliation unfinished.
	e.tick()
	readBudgetSnapshot = original
	committed := e.record()
	if release == nil || committed.CI != "passed" || committed.CIKey != normal.CIKey ||
		!strings.HasPrefix(committed.CIBudgetRuns["81/1"], "over CI time budget") {
		t.Fatalf("fixture missed settled CI with an unreconciled commit: CI=%s, key=%s, budgets=%v", committed.CI, committed.CIKey, committed.CIBudgetRuns)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	next := e.clock.Add(time.Minute)
	policy := EffectivePolicy(st, committed)
	if !pollDue(st, &committed, next) || fullPollDue(committed, next) ||
		!quietPull(committed, policy, e.f.Pull, committed.Polling.CIVersion, false, next) {
		t.Fatal("recovery fixture is not a due quiet poll before the safety slot")
	}
	release()
	if err := database.Close(filepath.Dir(e.s.Store.Root)); err != nil {
		t.Fatal(err)
	}
	reopened := *e.s
	reopened.tickReads = nil // A reopened watcher has no retained Tick rows.
	reopened.Store = &Store{Root: e.s.Store.Root}
	e.s = &reopened
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the remote head, checks, policy and update stamp unchanged.
	e.tick()
	if decodes.Load() == 0 {
		t.Fatal("recovery did not decode saved CI evidence")
	}
	recovered := e.record()
	if recovered.CI != "failed" || recovered.CIKey != "budget:"+recovered.Head+":81/1" || recovered.Failures != 1 ||
		recovered.Head != committed.Head || recovered.Polling.CIVersion != committed.Polling.CIVersion ||
		recovered.Polling.PullVersion != committed.Polling.PullVersion {
		t.Fatalf("next due quiet Tick did not reconcile once: CI=%s, key=%s, failures=%d, probe=%s",
			recovered.CI, recovered.CIKey, recovered.Failures, recovered.Polling.CIVersion)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/check-runs?") {
		t.Fatalf("recovery bypassed the normal full poll route: %s, %v", calls, err)
	}
	if fullPollDue(recovered, e.clock.Add(time.Minute)) {
		t.Fatal("repeat control reached the safety slot")
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	decodes.Store(0)
	e.tick()
	e.tick()
	after := e.record()
	calls, err = os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if after.CI != recovered.CI || after.CIKey != recovered.CIKey || after.Failures != 1 ||
		!after.Checked.Equal(recovered.Checked) || !after.Polling.Next.Equal(recovered.Polling.Next) ||
		len(lines) != 2 || !strings.Contains(lines[0], "graphql --include --method POST") ||
		!strings.Contains(lines[1], "graphql --include --method POST") || decodes.Load() != 0 {
		t.Fatalf("reconciled quiet PR repeated work: CI=%s, key=%s, failures=%d, checked=%s, saved CI decodes=%d, requests=%s",
			after.CI, after.CIKey, after.Failures, after.Checked, decodes.Load(), calls)
	}
}

func TestTickQuietIgnoresInactiveBudgetRun(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	e, _ := completedBudgetFixture(t)
	e.authorFeedOnly()
	r := e.record()
	prepareQuietBudgetTicks(t, e, r.Key)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.tick()
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.CIBudgetRuns["80/1"] = "over CI time budget (finished)"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := e.record()
	if before.CI != "passed" || fullPollDue(before, e.clock.Add(time.Minute)) {
		t.Fatal("inactive-run fixture is not a settled quiet PR")
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	decodes.Store(0)
	e.tick()
	e.tick()
	after := e.record()
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if after.CI != before.CI || after.CIKey != before.CIKey ||
		!after.Checked.Equal(before.Checked) || !after.Polling.Next.Equal(before.Polling.Next) ||
		len(lines) != 2 || !strings.Contains(lines[0], "graphql --include --method POST") ||
		!strings.Contains(lines[1], "graphql --include --method POST") || decodes.Load() != 2 {
		t.Fatalf("inactive run exceeded its run-evidence read: before=%s, after=%s, saved CI decodes=%d, requests=%s", before.CIKey, after.CIKey, decodes.Load(), calls)
	}
}
