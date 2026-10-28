package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/facts"
)

func TestUpdateRecordsOrAllRecoversUndeclaredRecordPolling(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	other := r
	other.Key, other.ChangeID, other.Number = "github.com/owner/repo#2", "github.com/owner/repo#2", 2
	other.Polling.Next = e.clock.Add(time.Hour)
	other.Polling.FullChecked = e.clock
	other.Polling.IntervalMinutes = 1
	other.QuotaAccount = "fixture"
	r.QuotaAccount = "fixture"
	scope := RateLimitScope{Host: "github.com", Account: "fixture", Resource: "core"}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key], st.Records[other.Key] = &r, &other
		st.RateLimits = map[string]RateLimit{scope.Key(): {PauseUntil: e.clock.Add(time.Hour), Reason: "rate limited", PauseScope: scope}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	available := 4995
	rates := cloneRates(st.RateLimits)
	rates[scope.Key()] = RateLimit{Remaining: &available, Reset: e.clock.Add(time.Hour), ObservedAt: e.clock.Add(time.Second)}
	if err := e.s.Store.updateRecordsOrAll([]string{r.Key}, func(current *State) bool {
		return ratesNeedAllRecords(current, rates, e.clock)
	}, func(current *State) error {
		applyRateLimits(current, rates, e.clock)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if next := saved.Records[other.Key].Polling.Next; !next.IsZero() {
		t.Fatalf("undeclared record kept its paused schedule %s; want immediate recovery", next)
	}
}

func TestHeadBatchReadinessSeesMergedDependency(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	dependency := r
	dependency.Key, dependency.ChangeID, dependency.Number = "github.com/owner/repo#2", "stable-dependency", 2
	dependency.State, dependency.Watching = "merged", false
	r.Dependencies = []string{dependency.ChangeID}
	r.DefaultBranch = "main"
	mergeable := true
	r.Mergeable, r.Checks = &mergeable, nil
	r.CI, r.Verdict, r.RoundHead, r.HeadVerifiedAt = "passed", "approve", r.Head, e.clock
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key], st.Records[dependency.Key] = &r, &dependency
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.SHA = r.Head
	e.write()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.observeHeadBatch(t.Context(), st, []*Record{&r}, func(current *State, heads map[string]headProbe) error {
		for key, probe := range heads {
			r := current.Records[key]
			observation := facts.FromRecord(*r)
			observation.Head, observation.Base = probe.Pull.Head.SHA, probe.Pull.Base.Ref
			noticeIfLandable(*current, r, observation)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range saved.Records[r.Key].Notices {
		if n.Kind == "land" && n.SupersededBy == "" {
			return
		}
	}
	current := saved.Records[r.Key]
	dependencyWaiting, waiting := facts.UnmergedDependency(*current, saved.Records)
	t.Fatalf("merged dependency outside the batch hid the ready notice: %+v landable=%v ready=%v dependency=%q/%v approved=%v headCurrent=%v", current.Notices, landable(saved, *current), facts.Ready(*current, facts.FromRecord(*current), saved.Records), dependencyWaiting, waiting, facts.ApprovedAtHead(*current), facts.HeadCurrent(*current, current.Head))
}
