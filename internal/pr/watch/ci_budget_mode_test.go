package watch

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDefaultRoomBudgetWarnsWithoutCancelling(t *testing.T) {
	e := newEnv(t)
	budgetFixture(e, e.clock.Add(-time.Hour), "pull_request")
	if err := e.s.Store.update(func(st *State) error { delete(st.Policies, e.author.RoomID); return nil }); err != nil {
		t.Fatal(err)
	}
	e.track()
	e.tick()
	r := e.record()
	if r.CI != "pending" {
		t.Fatalf("time budget changed CI to %s", r.CI)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "/cancel") {
		t.Fatal("warn mode cancelled CI")
	}
	found := false
	for _, n := range e.sent {
		if strings.Contains(n.Text, "Go tests") && n.Kind == "informational" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing quiet job warning: %+v", e.sent)
	}
}

func TestDefaultRoomTestBudgetWarnsWithoutFailing(t *testing.T) {
	e := newEnv(t)
	testReportFixture(t, e, map[string]string{"results.xml": `<testsuite><testcase name="slow" time="12"/></testsuite>`})
	if err := e.s.Store.update(func(st *State) error { delete(st.Policies, e.author.RoomID); return nil }); err != nil {
		t.Fatal(err)
	}
	e.track()
	e.tick()
	if e.record().CI != "passed" {
		t.Fatalf("slow test failed CI: %s", e.record().CI)
	}
	found := false
	for _, n := range e.sent {
		if strings.Contains(n.Text, "slow") && n.Kind == "informational" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing quiet test warning: %+v", e.sent)
	}
}

func TestWarnModeDropsQueuedBudgetFailureNotices(t *testing.T) {
	for _, kind := range []string{"run", "test"} {
		t.Run(kind, func(t *testing.T) {
			s := &Service{Store: &Store{Root: t.TempDir()}}
			r := Record{Key: "pr", URL: "https://github.com/o/r/pull/1", Head: "head", State: "open", Watching: true}
			if kind == "test" {
				notice(&r, "author", "CI for "+r.URL+" failed: tests over 10s: slow.", "test-budget:head:1/1")
			}
			if err := s.Store.update(func(st *State) error { st.Records[r.Key] = &r; return nil }); err != nil {
				t.Fatal(err)
			}
			if kind == "run" {
				snap := Snapshot{}
				snap.Pull.Head.SHA = r.Head
				if err := s.storeBudget(r, snap, "1/1", "over CI time budget (finished)", "CI for "+r.URL+" finished over budget: over CI time budget (finished).", false); err != nil {
					t.Fatal(err)
				}
			}
			assertNoticeDelivery(t, s, r.Key, "budget-policy-changed")
		})
	}
}
