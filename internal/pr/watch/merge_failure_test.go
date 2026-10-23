package watch

import (
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// A failed batch pauses its room and sends root one waking notice. A restart
// from the saved checkpoint does not repeat the notice.
func TestFailedBatchWakesRootOnce(t *testing.T) {
	e := newEnv(t)
	e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
	r := Record{Key: "change", RoomID: e.author.RoomID, Dir: e.author.Dir}
	b := state.MergeTrial{Branch: "trial/1", Head: "tested", Phase: "failed", RootReport: "TestBroken failed", Changes: []state.MergeTrialChange{{Key: r.Key}}}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{"repo": &b}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.stopMergeBatch(t.Context(), "repo", r, b, trialCI{}); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := saved.MergeTrials["repo"]
	if !got.Notified || got.Phase != "awaiting-root" || saved.MergePauses[r.RoomID] != mergeBatchFailedReason {
		t.Fatalf("trial = %+v, pauses = %v", got, saved.MergePauses)
	}
	if len(e.sent) != 1 || !e.sent[0].NeedsRoot || !strings.Contains(e.sent[0].Text, "TestBroken failed") {
		t.Fatalf("notices = %+v", e.sent)
	}
	restarted := *e.s
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *got, trialCI{}); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 {
		t.Fatal("restart repeated the notice")
	}
}

// A failed check of a run that is still going leaves no run log. The root notice
// must name that check, and its report must list it as a failed job.
func TestFailedBatchNoticeNamesTheFailedCheck(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The run ended cancelled, so no run log exists, but a check concluded a
	// real failure.
	f.ci, f.conclusion = "completed", "cancelled"
	f.checks = []Check{actionsCheck(Check{ID: 501, Name: "frontend-check", Status: "completed", Conclusion: "failure"})}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	ev, report := failedBatchNotice(t, f)
	if !strings.Contains(ev.Reason, "frontend-check") || ev.Checks == nil || len(ev.Checks.Failed) != 1 || ev.Checks.Failed[0] != "frontend-check" {
		t.Fatalf("notice reason=%q checks=%+v", ev.Reason, ev.Checks)
	}
	if !strings.Contains(report, "frontend-check") {
		t.Fatalf("report does not name the failed check:\n%s", report)
	}
}

// A failure that names no check and no run says so, and calls itself an
// infrastructure cause rather than a test failure.
func TestFailedBatchWithoutJobEvidenceReportsTheCause(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "cancelled"
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	ev, report := failedBatchNotice(t, f)
	if !strings.Contains(ev.Reason, "infrastructure") || !strings.Contains(report, "cancelled or never proved") {
		t.Fatalf("reason=%q report:\n%s", ev.Reason, report)
	}
}

// failedBatchNotice returns the one root notice of a failed batch and the trial's
// saved report, after checking that the trial waits for root.
func failedBatchNotice(t *testing.T, f *trialFixture) (room.MergeEvent, string) {
	t.Helper()
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials["github.com/owner/repo"]
	if b == nil || b.Phase != "awaiting-root" || b.Logs == "" {
		t.Fatalf("trial = %+v", b)
	}
	var found []Notice
	for _, n := range f.e.sent {
		if n.NeedsRoot {
			found = append(found, n)
		}
	}
	if len(found) != 1 || found[0].Merge == nil || found[0].Merge.Type != room.MergeFailed {
		t.Fatalf("root notices = %+v", found)
	}
	return *found[0].Merge, found[0].Text
}
