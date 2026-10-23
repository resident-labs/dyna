package watch

import (
	"context"
	"errors"
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
	// A delivery error must not lose the failure facts: the retry posts the same
	// cause and failed checks, even with no CI read of its own.
	deliver, unavailable := e.s.Deliver, true
	e.s.Deliver = func(ctx context.Context, rec Record, n Notice) (string, error) {
		if unavailable {
			return "", errors.New("room store unavailable")
		}
		return deliver(ctx, rec, n)
	}
	ci := trialCI{Status: "failed", Reason: "checks failed: TestBroken", Failed: []string{"TestBroken"}}
	if err := e.s.stopMergeBatch(t.Context(), "repo", r, b, ci); err == nil {
		t.Fatal("a delivery error was ignored")
	}
	if len(e.sent) != 0 {
		t.Fatalf("posted before delivery failed: %+v", e.sent)
	}
	saved, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	got := saved.MergeTrials["repo"]
	if got.Notified || got.Phase != "awaiting-root" || got.FailReason != ci.Reason || len(got.FailChecks) != 1 || got.FailChecks[0] != "TestBroken" {
		t.Fatalf("trial after a delivery error = %+v", got)
	}
	unavailable = false
	restarted := *e.s
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *got, trialCI{}); err != nil {
		t.Fatal(err)
	}
	posted, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 || !posted.MergeTrials["repo"].Notified {
		t.Fatalf("notices = %+v", e.sent)
	}
	if notice := e.sent[0]; notice.Merge == nil || notice.Merge.Reason != "" || notice.Merge.ReasonKind != room.MergeChecksFailed || notice.Merge.Checks == nil || len(notice.Merge.Checks.Failed) != 1 {
		t.Fatalf("retry notice lost the saved facts: %+v", notice.Merge)
	}
	// A restart resumes from the saved checkpoint, which is already notified.
	if err := restarted.stopMergeBatch(t.Context(), "repo", r, *posted.MergeTrials["repo"], trialCI{}); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 1 {
		t.Fatal("restart repeated the notice")
	}
}

// A failed push run can report failure without naming a check, even beside a
// cancelled workflow. Its logs are the evidence, so the notice and the report
// must keep the run failure and must not call it a cancellation.
func TestFailedBatchKeepsTheCauseOfAFailedRun(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	f.extraRuns = []Run{{ID: 82, Attempt: 1, WorkflowID: 2, Name: "second", Event: "push", HeadSHA: f.head, HeadBranch: f.branch, Status: "completed", Conclusion: "cancelled"}}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	ev, report := failedBatchNotice(t, f)
	if strings.Contains(ev.Reason, "infrastructure") || strings.Contains(report, "was cancelled") {
		t.Fatalf("a failed run was called a cancellation: reason=%q\n%s", ev.Reason, report)
	}
	if ev.Reason != "a CI run failed without a named check; read its logs for the failing test" {
		t.Fatalf("reason=%q", ev.Reason)
	}
	if !strings.Contains(report, "unknown; job evidence unavailable") {
		t.Fatalf("report does not keep the unknown job name:\n%s", report)
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
	if ev.Reason != "" || ev.ReasonKind != room.MergeChecksFailed || ev.Checks == nil || len(ev.Checks.Failed) != 1 || ev.Checks.Failed[0] != "frontend-check" {
		t.Fatalf("notice reason=%q kind=%q checks=%+v", ev.Reason, ev.ReasonKind, ev.Checks)
	}
	if !strings.Contains(report, "frontend-check") {
		t.Fatalf("report does not name the failed check:\n%s", report)
	}
}

// A failed batch names every member PR, so the card names the batch rather than
// the one PR the trial was anchored on. Its cause is the typed kind alone: the
// failing check names are the evidence, and the event repeats no reason text.
func TestFailedBatchEventNamesEveryMember(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2)
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.ci, f.conclusion = "completed", "failure"
	f.checks = []Check{actionsCheck(Check{ID: 501, Name: "go-test", Status: "completed", Conclusion: "failure"})}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	ev, _ := failedBatchNotice(t, f)
	var numbers []int
	for _, pr := range ev.PRs {
		numbers = append(numbers, pr.Number)
	}
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Fatalf("member PRs = %v, want both members", numbers)
	}
	if ev.Reason != "" || ev.ReasonKind != room.MergeChecksFailed {
		t.Fatalf("cause = %q/%q, want the checks-failed kind alone", ev.Reason, ev.ReasonKind)
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
	if !strings.Contains(ev.Reason, "infrastructure") || !strings.Contains(report, "was cancelled") {
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
