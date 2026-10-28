package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/facts"
)

func headBatchFixtureBody(r *Record, at time.Time) []byte {
	repo := map[string]any{"defaultBranchRef": map[string]string{"name": "main"}}
	repo[fmt.Sprintf("p%d", r.Number)] = map[string]any{
		"number": r.Number, "title": "Ready", "state": "OPEN", "merged": false,
		"updatedAt": at.Format(time.RFC3339), "headRefOid": r.Head, "headRefName": "feature",
		"baseRefOid": strings.Repeat("c", 40), "baseRefName": "main", "author": map[string]string{"login": "owner", "__typename": "User"},
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]string{"state": "SUCCESS"}}}}},
	}
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": repo, "rateLimit": map[string]any{"remaining": 4999, "resetAt": at.Add(time.Hour).Format(time.RFC3339)}}})
	return body
}

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
	dependency.Key, dependency.ChangeID, dependency.Number = "github.com/owner/repo#2", "github.com/owner/repo#2", 2
	dependency.State, dependency.Watching = "merged", false
	r.Dependencies = []string{dependency.Key}
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
	body := headBatchFixtureBody(&r, e.clock)
	e.s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
		_, err := cmd.Stdout.Write(body)
		return err
	}
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
