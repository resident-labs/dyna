package watch

import (
	"context"
	"dyna-agent/internal/pr/facts"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestBatchedHeadsOwnReadinessFacts(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	completed := e.clock.Add(-time.Minute)
	statusAt := completed.Add(time.Second)
	node := map[string]any{"number": r.Number, "title": "Facts", "state": "OPEN", "headRefOid": r.Head, "baseRefName": "main", "isDraft": true, "mergeable": "CONFLICTING",
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "FAILURE", "contexts": map[string]any{"nodes": []any{map[string]any{"completedAt": completed}, map[string]any{"createdAt": statusAt}}}}}}}}}
	save := func() {
		b, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": map[string]any{"defaultBranchRef": map[string]string{"name": "main"}, fmt.Sprint("p", r.Number): node}}})
		e.f.Raw["graphql"] = "HTTP/2.0 200 OK\r\n\r\n" + string(b)
		e.write()
	}
	save()
	st, _ := e.s.Store.Read()
	if _, err := e.s.refreshHeads(context.Background(), st, e.clock, nil); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if !got.IsDraft || got.Mergeable == nil || *got.Mergeable || got.DefaultBranch != "main" || !got.ChecksAt.Equal(statusAt) || got.CI != "failed" {
		t.Fatalf("remote observations not saved: %+v", got)
	}
	node["isDraft"], node["mergeable"] = false, "UNKNOWN"
	save()
	st, _ = e.s.Store.Read()
	if _, err := e.s.refreshHeads(context.Background(), st, e.clock, nil); err != nil {
		t.Fatal(err)
	}
	got = e.record()
	if got.IsDraft || got.Mergeable != nil {
		t.Fatal("unknown remote mergeability inherited old fact")
	}
}

func TestSnapshotFactsDoNotInheritSavedGreenCI(t *testing.T) {
	yes := true
	e := newEnv(t)
	r := e.track()
	r.CI, r.Verdict, r.RoundHead = "passed", "approve", r.Head
	r.Mergeable = &yes
	r.DefaultBranch = "main"
	snap := Snapshot{Pull: e.f.Pull, Checks: []Check{actionsCheck(Check{ID: 1, Name: "test", Status: "in_progress"})}}
	if g := snap.GitHubFacts(r); g.CI != "pending" || facts.Ready(r, g, nil) {
		t.Fatalf("fresh pending CI inherited saved green: %+v", g)
	}
	snap.Checks[0].Status, snap.Checks[0].Conclusion = "completed", "failure"
	if g := snap.GitHubFacts(r); g.CI != "failed" || facts.Ready(r, g, nil) {
		t.Fatalf("fresh failed CI inherited saved green: %+v", g)
	}
	snap.Checks[0].Conclusion = "success"
	if g := snap.GitHubFacts(r); !facts.Ready(r, g, nil) {
		t.Fatalf("fresh passing CI not ready: %+v", g)
	}
	snap.Pull.Head.SHA = strings.Repeat("b", 40)
	if g := snap.GitHubFacts(r); facts.Ready(r, g, nil) {
		t.Fatal("new head inherited approval")
	}
}
