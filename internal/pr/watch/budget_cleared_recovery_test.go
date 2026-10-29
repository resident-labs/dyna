package watch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

// Commit a real replacement measurement while the existing gate holds its
// reconciliation. The next Tick uses a reopened database and unchanged probes.
func interruptedBudgetReplacement(t *testing.T, status, conclusion, start, outcome string) *testEnv {
	t.Helper()
	e, _ := overBudgetFixture(t)
	e.authorFeedOnly()
	prepareQuietBudgetTicks(t, e, e.record().Key)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.f.Runs[0].Status, e.f.Runs[0].Conclusion = status, conclusion
	e.tick()
	failed := e.record()
	if failed.CI != "failed" || failed.CIKey != "budget:"+failed.Head+":81/1" || failed.Failures != 1 {
		t.Fatalf("fixture did not process its initial budget failure: CI=%s, key=%s, failures=%d", failed.CI, failed.CIKey, failed.Failures)
	}
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records[failed.Key]
		r.Failures, r.CIPaused, r.PauseReason = 3, true, "CI wakes paused after 3 consecutive failures"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unlock, owned, err := e.s.tryPublicationGate(failed.Key)
	if err != nil || !owned {
		t.Fatalf("hold replacement gate: owned=%v, error=%v", owned, err)
	}
	release := sync.OnceFunc(unlock)
	t.Cleanup(release)
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	e.f.Raw[path] = strings.ReplaceAll(e.f.Raw[path], "2026-10-17T11:59:40Z", start)
	e.write()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	gh := e.s.github(failed.RoomID)
	gh.requests = &requestCache{entries: st.Responses[failed.Key], used: map[string]cachedResponse{},
		rates: cloneRates(st.RateLimits), now: e.s.now, forceBudgets: true}
	snap := Snapshot{Pull: e.f.Pull, Runs: e.f.Runs}
	if err := e.s.runBudgets(t.Context(), pollResult{key: failed.Key, gh: gh, snap: snap}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	committed := e.record()
	if committed.CIBudgetRuns["81/1"] != outcome || committed.CI != "failed" || committed.CIKey != failed.CIKey ||
		committed.Failures != 3 || !committed.CIPaused || fullPollDue(committed, e.clock.Add(time.Minute)) {
		t.Fatalf("replacement did not stop before reconciliation: outcome=%s, CI=%s, key=%s, failures=%d, paused=%v",
			committed.CIBudgetRuns["81/1"], committed.CI, committed.CIKey, committed.Failures, committed.CIPaused)
	}
	release()
	if err := database.Close(filepath.Dir(e.s.Store.Root)); err != nil {
		t.Fatal(err)
	}
	reopened := *e.s
	reopened.Store = &Store{Root: e.s.Store.Root}
	e.s = &reopened
	return e
}

func requireQuietRecoveryRepeats(t *testing.T, e *testEnv, before Record, decodes *atomic.Int64, expectedDecodes int64) {
	t.Helper()
	if fullPollDue(before, e.clock.Add(time.Minute)) || fullPollDue(before, e.clock.Add(2*time.Minute)) {
		t.Fatal("repeat control reached the safety slot")
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
	if after.CI != before.CI || after.CIKey != before.CIKey || after.CIReason != before.CIReason ||
		after.Failures != before.Failures || after.CIPaused != before.CIPaused || after.PauseReason != before.PauseReason ||
		!after.Checked.Equal(before.Checked) || !after.Polling.Next.Equal(before.Polling.Next) ||
		len(lines) != 2 || !strings.Contains(lines[0], "graphql --include --method POST") ||
		!strings.Contains(lines[1], "graphql --include --method POST") || decodes.Load() != expectedDecodes {
		t.Fatalf("quiet recovery repeated work: CI=%s, key=%s, failures=%d, paused=%v, saved CI decodes=%d, requests=%s",
			after.CI, after.CIKey, after.Failures, after.CIPaused, decodes.Load(), calls)
	}
}

func requireClearedBudgetRecovery(t *testing.T, start, outcome string) {
	t.Helper()
	decodes := countSavedCIDecodes(t)
	e := interruptedBudgetReplacement(t, "completed", "success", start, outcome)
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	before := e.record()
	if !quietPull(before, EffectivePolicy(st, before), e.f.Pull, before.Polling.CIVersion, false, e.clock.Add(time.Minute)) {
		t.Fatal("replacement recovery is not a due quiet poll")
	}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.tick()
	recovered := e.record()
	ci, key, _, _ := (Snapshot{Pull: e.f.Pull, Runs: e.f.Runs}).CIStatus()
	if ci != "passed" || string(recovered.CI) != ci || recovered.CIKey != key || recovered.CIReason != "" ||
		recovered.Failures != 0 || recovered.CIPaused || recovered.PauseReason != "" || recovered.CIBudgetRuns["81/1"] != outcome {
		t.Fatalf("cleared budget did not reconcile passing CI and reset failures: CI=%s, key=%s, failures=%d, paused=%v, outcome=%s",
			recovered.CI, recovered.CIKey, recovered.Failures, recovered.CIPaused, recovered.CIBudgetRuns["81/1"])
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/check-runs?") {
		t.Fatalf("replacement bypassed normal full polling: %s, %v", calls, err)
	}
	requireQuietRecoveryRepeats(t, e, recovered, decodes, 0)
}

func TestTickRecoversWithinBudgetAfterReopen(t *testing.T) {
	requireClearedBudgetRecovery(t, "2026-10-17T11:59:59Z", "within budget")
}

func TestTickRecoversWarnedBudgetAfterReopen(t *testing.T) {
	requireClearedBudgetRecovery(t, "2026-10-17T11:59:58Z", "warned")
}

func TestTickClearedBudgetConvergesOnCancelledCI(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	e := interruptedBudgetReplacement(t, "completed", "cancelled", "2026-10-17T11:59:59Z", "within budget")
	before := e.record()
	e.tick()
	after := e.record()
	if after.CI != "failed" || after.CIKey != before.CIKey || after.Failures != before.Failures || !after.CIPaused ||
		after.CIReason != "The last CI run was cancelled. Push or rerun CI to test this commit." {
		t.Fatalf("cancellation changed the preserved key or failure state: CI=%s, key=%s, failures=%d, paused=%v, reason=%s",
			after.CI, after.CIKey, after.Failures, after.CIPaused, after.CIReason)
	}
	// The reconciled cancellation keeps its budget key without scheduling
	// another full poll or decoding saved CI on either quiet repeat.
	requireQuietRecoveryRepeats(t, e, after, decodes, 0)
}

func TestTickClearedBudgetPreservesPendingPolling(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	e := interruptedBudgetReplacement(t, "in_progress", "", "2026-10-17T11:59:59Z", "within budget")
	before := e.record()
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	decodes.Store(0)
	for range 2 {
		e.tick()
		after := e.record()
		if after.CI != "pending" || after.CIKey != before.CIKey || after.Failures != before.Failures || !after.CIPaused {
			t.Fatalf("pending CI lost normal key or failure state: CI=%s, key=%s, failures=%d, paused=%v", after.CI, after.CIKey, after.Failures, after.CIPaused)
		}
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil || strings.Count(string(calls), "/check-runs?") != 2 || decodes.Load() != 0 {
		t.Fatalf("pending CI left normal polling: saved CI decodes=%d, requests=%s, error=%v", decodes.Load(), calls, err)
	}
	e.f.Runs[0].Status, e.f.Runs[0].Conclusion = "completed", "success"
	e.tick()
	after := e.record()
	if _, budget := ParseBudgetFailureKey(after.CIKey); after.CI != "passed" || budget || after.Failures != 0 || after.CIPaused || after.PauseReason != "" {
		t.Fatalf("pending CI did not converge after success: CI=%s, key=%s, failures=%d, paused=%v", after.CI, after.CIKey, after.Failures, after.CIPaused)
	}
}

func TestTickCandidateFreeQuietAddsNoRecoveryWork(t *testing.T) {
	decodes := countSavedCIDecodes(t)
	e, _ := completedBudgetFixture(t)
	e.authorFeedOnly()
	prepareQuietBudgetTicks(t, e, e.record().Key)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.tick()
	before := e.record()
	if _, budget := ParseBudgetFailureKey(before.CIKey); before.CI != "passed" || budget {
		t.Fatal("clean quiet fixture has a pending recovery")
	}
	requireQuietRecoveryRepeats(t, e, before, decodes, 0)
}
