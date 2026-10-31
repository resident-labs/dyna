package watch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTerminalWatchRecoversReopenedPull(t *testing.T) {
	e := newEscalationEnv(t)
	r := e.testEnv.track()
	e.tick()
	if err := e.s.Store.update(func(st *State) error {
		endRecord(st.Records[r.Key], false, e.clock)
		st.Records[r.Key].Polling.Next = time.Time{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.tick()
	got := e.record()
	if got.State != "open" || !got.Watching || !got.ClosedAt.IsZero() {
		t.Fatalf("reopened watch did not recover: %+v", got)
	}
	var oldID string
	for _, n := range got.Notices {
		if n.Kind == "closed" {
			oldID = n.ID
			if n.SupersededBy != "pr-reopened" {
				t.Fatalf("old close notice remains active: %+v", n)
			}
		}
	}
	// Pruning old notices must not make the next close reuse its ID.
	got.Notices = nil
	endRecord(&got, false, e.clock.Add(time.Minute))
	for _, n := range got.Notices {
		if n.Kind == "closed" && n.ID != oldID && n.SupersededBy == "" {
			return
		}
	}
	t.Fatal("second closure did not create a new notice")
}

func TestTerminalWatchRecoveryBacksOffAndFinishes(t *testing.T) {
	e := newEscalationEnv(t)
	r := e.testEnv.track()
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.State, e.f.Pull.Merged = "closed", true
	e.f.Pull.Head.Ref = "feature"
	e.s.CleanupMerged = func(context.Context, Record, string) error { return nil }
	if err := e.s.Store.update(func(st *State) error {
		endRecord(st.Records[r.Key], true, e.clock)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Fail = "/pulls/17"
	e.write()
	_, _ = e.s.Tick(t.Context())
	first := e.record()
	if !first.Watching || !first.Polling.Next.After(e.clock) {
		t.Fatalf("failed recovery lost watch or retry: %+v", first)
	}
	delay := first.Polling.Next.Sub(e.clock)
	if delay <= 0 || delay > time.Hour {
		t.Fatalf("invalid retry delay %s", delay)
	}
	// A restart retains the saved deadline; an early tick must not query GitHub.
	before, _ := os.ReadFile(e.calls)
	e.clock = first.Polling.Next.Add(-time.Second)
	e.write()
	_, _ = e.s.Tick(t.Context())
	after, _ := os.ReadFile(e.calls)
	if len(after) != len(before) {
		t.Fatalf("early retry made GitHub calls: %s", after[len(before):])
	}
	e.clock = first.Polling.Next
	e.write()
	_, _ = e.s.Tick(t.Context())
	second := e.record()
	if second.Polling.Next.Sub(e.clock) <= delay || second.Polling.Next.Sub(e.clock) > time.Hour {
		t.Fatalf("retry did not back off: first %s, next %s", delay, second.Polling.Next.Sub(e.clock))
	}
	e.f.Fail = ""
	e.clock = second.Polling.Next
	e.write()
	if _, err := e.s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if got.Watching || got.State != "merged" || !got.CICleanupDone {
		t.Fatalf("terminal watch did not finish: %+v", got)
	}
	calls, _ := os.ReadFile(e.calls)
	if strings.Contains(string(calls), "graphql") {
		t.Fatalf("terminal recovery entered head batch: %s", calls)
	}
}
