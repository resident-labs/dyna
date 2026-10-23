package watch

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestFlushChecksNoticeAtDelivery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Record)
		reason string
	}{
		{"landed", func(r *Record) { r.State = "merged"; r.Watching = false }, "change-ended"},
		{"closed", func(r *Record) { r.State = "closed" }, "change-ended"},
		{"new head", func(r *Record) { r.Head = "new" }, "head-changed"},
		{"approval removed", func(r *Record) { r.Verdict = "changes" }, "no-longer-landable"},
		{"current", func(r *Record) {}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Store: &Store{Root: t.TempDir()}}
			r := Record{Key: "pr", Head: "head", RoundHead: "head", State: "open", Watching: true, CI: "passed", Verdict: "approve", BaseRef: "main", DefaultBranch: "main", Mergeable: readyMergeability()}
			noticeIfLandable(State{}, &r)
			if len(r.Notices) != 1 {
				t.Fatal("ready notice was not queued")
			}
			tc.change(&r)
			if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
				t.Fatal(err)
			}
			assertNoticeDelivery(t, s, r.Key, tc.reason)
		})
	}
}

func TestFlushDropsRemeasuredBudgetNotice(t *testing.T) {
	for _, verdict := range []string{"warned", "within budget", "over CI time budget (finished)"} {
		t.Run(verdict, func(t *testing.T) {
			s := &Service{Store: &Store{Root: t.TempDir()}}
			r := Record{Key: "pr", URL: "https://github.com/o/r/pull/1", Head: "head", State: "open", Watching: true}
			snap := Snapshot{}
			snap.Pull.Head.SHA = r.Head
			if err := s.Store.update(func(st *State) error {
				st.Records[r.Key] = &r
				p := RoomPolicy(*st, r.RoomID)
				p.CITimeBudget = "enforce"
				if st.Policies == nil {
					st.Policies = map[string]ChangePolicy{}
				}
				st.Policies[r.RoomID] = p
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			reason := "over CI time budget (finished)"
			text := fmt.Sprintf("CI for %s finished over budget: %s. Slowest jobs/steps: tests (4 minutes)", r.URL, reason)
			if err := s.storeBudget(r, snap, "123/1", reason, text, false); err != nil {
				t.Fatal(err)
			}
			if err := s.storeBudget(r, snap, "123/1", verdict, "", false); err != nil {
				t.Fatal(err)
			}
			dropped := "budget-verdict-changed"
			if verdict == reason {
				dropped = ""
			}
			assertNoticeDelivery(t, s, r.Key, dropped)
		})
	}
}

func assertNoticeDelivery(t *testing.T, s *Service, key, reason string) {
	t.Helper()
	calls := 0
	s.Deliver = func(context.Context, Record, Notice) (string, error) { calls++; return "post", nil }
	if err := s.flush(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	r, err := s.Store.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	n := r.Notices[0]
	want := 1
	if reason != "" {
		want = 0
	}
	if calls != want || n.SupersededBy != reason || n.Done != (reason == "") {
		t.Fatalf("calls=%d, notice=%+v; want %d deliveries and drop reason %q", calls, n, want, reason)
	}
}

func TestFlushReloadsBetweenDeliveries(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	r := Record{Key: "pr", Head: "head", RoundHead: "head", State: "open", Watching: true, CI: "passed", Verdict: "approve", BaseRef: "main", DefaultBranch: "main", Mergeable: readyMergeability()}
	notice(&r, "author", "First notice", "first")
	noticeIfLandable(State{}, &r)
	if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Deliver = func(context.Context, Record, Notice) (string, error) {
		calls++
		return "post", s.Store.update(func(st *State) error { st.Records[r.Key].State = "merged"; return nil })
	}
	if err := s.flush(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	got, err := s.Store.Get(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Notices[1].SupersededBy != "change-ended" {
		t.Fatalf("deliveries=%d, notices=%+v", calls, got.Notices)
	}
}

func TestFlushKeepsTransferredBudgetNotice(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	r := Record{Key: "pr", URL: "https://github.com/o/r/pull/1", Author: "old", Head: "head", State: "open", Watching: true,
		CIBudgetHead: "head", CIBudgetRuns: map[string]string{"123/1": "over CI time budget (finished)"}}
	notice(&r, "old", "CI for "+r.URL+" finished over budget: over CI time budget (finished). Slowest jobs/steps: tests", "ci-budget:head:123/1:over CI time budget (finished)")
	replacement := r.Notices[0]
	replacement.ID = "msg_" + digest(replacement.ID + "/transfer/new")[:16]
	replacement.Target = "new"
	r.Notices[0].SupersededBy = replacement.ID
	r.Notices = append(r.Notices, replacement)
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		p := RoomPolicy(*st, r.RoomID)
		p.CITimeBudget = "enforce"
		st.Policies = map[string]ChangePolicy{r.RoomID: p}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		calls++
		if n.ID != replacement.ID || n.Target != "new" {
			t.Fatalf("wrong replacement: %+v", n)
		}
		return "post", nil
	}
	if err := s.flush(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("deliveries=%d, want 1", calls)
	}
}

func TestFlushPreservesCommentsAcrossStateChanges(t *testing.T) {
	for _, state := range []string{"open", "closed", "merged"} {
		t.Run(state, func(t *testing.T) {
			s := &Service{Store: &Store{Root: t.TempDir()}}
			r := Record{Key: "pr", Head: "old", State: "open", Watching: true}
			notice(&r, "author", "GitHub comment from owner (comment 1):\nPlease keep this feedback.", "batch:1")
			r.Head, r.State = "new", state
			if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
				t.Fatal(err)
			}
			assertNoticeDelivery(t, s, r.Key, "")
		})
	}
}

func TestFlushRestoresReadyNoticeAfterCIRerun(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	r := Record{Key: "pr", Head: "head", RoundHead: "head", State: "open", Watching: true, CI: "passed", Verdict: "approve", BaseRef: "main", DefaultBranch: "main", Mergeable: readyMergeability()}
	noticeIfLandable(State{}, &r)
	if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Deliver = func(context.Context, Record, Notice) (string, error) { return "", fmt.Errorf("delivery unavailable") }
	if err := s.flush(context.Background(), r.Key); err == nil {
		t.Fatal("delivery should fail")
	}
	if err := s.Store.update(func(st *State) error { st.Records[r.Key].CI = "pending"; return nil }); err != nil {
		t.Fatal(err)
	}
	assertNoticeDelivery(t, s, r.Key, "no-longer-landable")
	restore := func() {
		t.Helper()
		if err := s.Store.update(func(st *State) error {
			current := st.Records[r.Key]
			current.CI = "passed"
			noticeIfLandable(*st, current)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	restore()
	assertNoticeDelivery(t, s, r.Key, "")
	restore()
	calls := 0
	s.Deliver = func(context.Context, Record, Notice) (string, error) { calls++; return "post", nil }
	if err := s.flush(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("delivered readiness was queued again")
	}
}

func TestFlushKeepsCloseNotice(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.f.Pull.State = "closed"
	e.tick()
	if len(e.sent) != 1 || e.sent[0].Kind != "closed" || !strings.Contains(e.sent[0].Text, "PR #17 closed.") {
		t.Fatalf("close notice was lost: %+v", e.sent)
	}
}

func readyMergeability() *bool { yes := true; return &yes }
