package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	prstate "dyna-agent/internal/pr/state"
	appstate "dyna-agent/internal/state"
)

func completedBudgetFixture(t *testing.T) (*testEnv, Snapshot) {
	t.Helper()
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-20*time.Second), "pull_request")
	e.f.Runs[0].Status, e.f.Runs[0].Conclusion = "completed", "success"
	e.f.Raw["repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"] = `HTTP/2.0 200 OK` + "\r\n\r\n" + `{"jobs":[{"name":"Go tests","status":"completed","created_at":"2026-10-17T11:59:38Z","started_at":"2026-10-17T11:59:40Z","completed_at":"2026-10-17T12:00:00Z","steps":[{"name":"Test","status":"completed","started_at":"2026-10-17T11:59:40Z","completed_at":"2026-10-17T12:00:00Z"}]}]}`
	e.f.Raw["repos/owner/repo/actions/runs/81/artifacts?per_page=100"] = "HTTP/2.0 200 OK\r\n\r\n{\"artifacts\":[]}"
	e.write()
	e.track()
	return e, Snapshot{Pull: e.f.Pull, Runs: e.f.Runs}
}

func runBudgetFixture(t *testing.T, e *testEnv, snap Snapshot) {
	t.Helper()
	// Build the poll result as Tick already does, outside the work we count.
	st, err := prstate.Read(e.s.Store.Root)
	if err != nil {
		t.Fatal(err)
	}
	key := "github.com/owner/repo#17"
	gh := e.s.github(e.author.RoomID)
	gh.requests = &requestCache{entries: st.Responses[key], used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now}
	if err := e.s.runBudgets(t.Context(), pollResult{key: key, gh: gh, snap: snap}, map[string]bool{}); err != nil {
		t.Fatal(err)
	}
}

func TestUnchangedBudgetInputsDoNoRepeatedWork(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	runBudgetFixture(t, e, snap)
	db, err := database.Open(filepath.Dir(e.s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	writer := appstate.For(db)
	before := writer.Health().Writes
	var published atomic.Int64
	writer.SetPublish(func(ev hub.Event) {
		if ev.Topic == prstate.Topic {
			published.Add(1)
		}
	})
	original := readStore
	wholeCopies := 0
	readStore = func(root string) (State, error) { wholeCopies++; return original(root) }
	t.Cleanup(func() { readStore = original })
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		runBudgetFixture(t, e, snap)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if wholeCopies != 0 || writer.Health().Writes != before || published.Load() != 0 || strings.TrimSpace(string(calls)) != "" {
		t.Fatalf("unchanged budgets repeated work: full copies=%d, writer calls=%d, publications=%d, requests=%s", wholeCopies, writer.Health().Writes-before, published.Load(), calls)
	}
}

func TestBudgetPreparationKeepsConcurrentRecordAndPolicyChanges(t *testing.T) {
	for _, field := range []string{"author", "room", "head", "policy", "attempt", "base"} {
		t.Run(field, func(t *testing.T) {
			e, snap := completedBudgetFixture(t)
			r := e.record()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			policy := EffectivePolicy(st, r)
			gh := e.s.github(r.RoomID)
			original := gh.run
			changed := false
			gh.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if !changed && strings.Contains(strings.Join(cmd.Args, " "), "/jobs?") {
					changed = true
					if err := e.s.Store.update(func(st *State) error {
						live := st.Records[r.Key]
						switch field {
						case "author":
							live.Author = "another-author"
						case "room":
							live.RoomID = "another-room"
						case "head":
							live.Head = strings.Repeat("b", 40)
						case "policy":
							next := policy
							next.CIWarnAfter, next.CIFailAfter = "5m", "10m"
							st.Policies[live.RoomID] = next
						case "attempt":
							live.Polling.CIVersion = "new-attempt"
						case "base":
							live.BaseRef = "release"
						}
						return nil
					}); err != nil {
						return err
					}
				}
				return original(ctx, cmd)
			}
			if err := e.s.enforceCIBudget(t.Context(), gh, r, snap, policy); err != nil {
				t.Fatal(err)
			}
			after, err := e.s.Store.Get(r.Key)
			if err != nil || !changed || after.CIBudgetHead != "" || len(after.CIQueueWait) != 0 {
				t.Fatalf("stale budget committed over %s: changed=%v, budget head=%s, queue=%v, error=%v", field, changed, after.CIBudgetHead, after.CIQueueWait, err)
			}
		})
	}
}

func TestBudgetMeasurementCommitsQueueAndInputOnce(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	r := e.record()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(e.s.Store.Root))
	if err != nil {
		t.Fatal(err)
	}
	writer := appstate.For(db)
	before := writer.Health().Writes
	var published atomic.Int64
	writer.SetPublish(func(ev hub.Event) {
		if ev.Topic == prstate.Topic {
			published.Add(1)
		}
	})
	gh := e.s.github(r.RoomID)
	gh.requests = &requestCache{used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now}
	if err := e.s.enforceCIBudget(t.Context(), gh, r, snap, EffectivePolicy(st, r)); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	// One commit for the verdict, and one for the CI health row that keeps this
	// run, its jobs and its queue wait for the CI health view. The two are
	// different tables in one database under one writer, so they cannot share a
	// transaction. Each still commits once per measurement, never twice.
	if got.CIQueueWait["81/1"] != "2s" || got.CIBudgetInputs["81/1"] == "" || writer.Health().Writes-before != 2 || published.Load() != 1 {
		t.Fatalf("one measurement did not commit once per table: queue=%v, input=%v, writes=%d, publications=%d", got.CIQueueWait, got.CIBudgetInputs, writer.Health().Writes-before, published.Load())
	}
}

func TestUnrelatedPRWriteDoesNotRepeatTargetBudgetWork(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	runBudgetFixture(t, e, snap)
	beforeTarget := e.record()
	other := e.record()
	other.Key, other.Number, other.Title = "github.com/owner/repo#18", 18, "before unrelated write"
	if err := e.s.Store.update(func(st *State) error {
		st.Records[other.Key] = &other
		return nil
	}); err != nil {
		t.Fatal(err)
	}
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
	originalSnapshot, originalRead := readBudgetSnapshot, readStore
	injected, reprocessingReads := false, 0
	readBudgetSnapshot = func(root string) (prstate.Snapshot, error) {
		observed, err := originalSnapshot(root)
		if err == nil && !injected {
			injected = true
			// Commit after retaining the old snapshot and before its target pass.
			err = e.s.Store.update(func(st *State) error {
				st.Records[other.Key].Title = "after unrelated write"
				return nil
			})
		}
		return observed, err
	}
	readStore = func(root string) (State, error) {
		reprocessingReads++
		return originalRead(root)
	}
	t.Cleanup(func() { readBudgetSnapshot, readStore = originalSnapshot, originalRead })
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	runBudgetFixture(t, e, snap)
	afterTarget := e.record()
	afterOther, err := e.s.Store.Get(other.Key)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil {
		t.Fatal(err)
	}
	if !injected || afterOther.Title != "after unrelated write" || !reflect.DeepEqual(beforeTarget, afterTarget) ||
		writer.Health().Writes-beforeWrites != 1 || published.Load() != 1 || reprocessingReads != 0 || strings.TrimSpace(string(calls)) != "" {
		t.Fatalf("unrelated write repeated target work: injected=%v, target changed=%v, writer calls=%d, publications=%d, reprocessing reads=%d, requests=%s",
			injected, !reflect.DeepEqual(beforeTarget, afterTarget), writer.Health().Writes-beforeWrites, published.Load(), reprocessingReads, calls)
	}
}
