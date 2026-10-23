package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
)

// TestReviewResultRouting keeps today's recipients and wake rules: a changes
// result wakes the author, and every other result is a quiet room post.
func TestReviewResultRouting(t *testing.T) {
	cases := []struct {
		name       string
		result     ReviewResult
		wantTarget func(Record) string
		wantKind   string
		wantNote   string
	}{
		{"approve", ReviewResult{Verdict: state.VerdictApprove, Summary: "Looks good."}, func(Record) string { return "" }, "approve", ""},
		{"changes", ReviewResult{Verdict: state.VerdictChanges, Summary: "Please fix bounds.", Findings: []Finding{{Severity: state.FindingBlocking, File: "a.go", Line: 3, Why: "bounds", Fix: "check"}}}, func(r Record) string { return r.Author }, "changes", ""},
		{"stop-early", ReviewResult{Verdict: state.VerdictStopEarly, StopReason: state.StopSuperseded}, func(Record) string { return "" }, "stop-early", ""},
		// A GitHub PR has no path read in this fixture, so a fast approval
		// falls back to the normal path instead of claiming a fast landing.
		{"approve-fast falls back without paths", ReviewResult{Verdict: state.VerdictApproveFast, Summary: "Safe."}, func(Record) string { return "" }, "approve", "normal path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, r := namedReviewEnv(t)
			before := len(e.sent)
			c.result.Head = r.Head
			if err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, c.result); err != nil {
				t.Fatal(err)
			}
			if len(e.sent) != before+1 {
				t.Fatalf("delivered %d notices, want 1: %+v", len(e.sent)-before, e.sent[before:])
			}
			n := e.sent[len(e.sent)-1]
			if n.Review == nil || string(n.Review.Kind) != c.wantKind || n.Target != c.wantTarget(r) {
				t.Fatalf("notice = %+v (review %+v)", n, n.Review)
			}
			if n.Review.Note != c.wantNote {
				t.Fatalf("fast-land note = %q, want %q", n.Review.Note, c.wantNote)
			}
			if c.wantKind == "changes" {
				if n.Review.Blocking != 1 || len(n.Review.Findings) != 1 {
					t.Fatalf("finding counts = %+v", n.Review)
				}
			}
		})
	}
}

// TestReviewResultNeedsATypedResult makes a turn without a result an error, so
// a reviewer cannot end silently.
func TestReviewResultNeedsATypedResult(t *testing.T) {
	e, r := namedReviewEnv(t)
	err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, ReviewResult{Head: r.Head, Summary: "Reviewed."})
	if err == nil || !strings.Contains(err.Error(), "approve, changes, approve-fast, or stop-early") {
		t.Fatalf("untyped result = %v", err)
	}
	if got := e.record(); got.Verdict != "" || len(got.ReviewRounds) != 0 {
		t.Fatalf("rejected result changed state: %+v", got)
	}
}

// TestStopEarlyRecordsItsReason keeps the stop reason as a typed fact, and does
// not let the stop judge the change.
func TestStopEarlyRecordsItsReason(t *testing.T) {
	e, r := namedReviewEnv(t)
	if err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, ReviewResult{Verdict: state.VerdictStopEarly, StopReason: state.StopTooLarge, Head: r.Head}); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if got.Verdict != "" || got.StopReason != state.StopTooLarge || len(got.ReviewRounds) != 1 || got.ReviewRounds[0].StopReason != state.StopTooLarge {
		t.Fatalf("stop-early result: %+v", got)
	}
}

// TestFastLandFollowsTheChangedPaths proves the path rule decides and the
// reviewer's word alone never does.
func TestFastLandFollowsTheChangedPaths(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "base")
	git("checkout", "-b", "docs")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "map.md"), []byte("# Map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "docs")
	docsHead := git("rev-parse", "HEAD")
	git("checkout", "main")
	git("checkout", "-b", "code")
	if err := os.WriteFile(filepath.Join(dir, "map.go"), []byte("package mapx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "code")

	git("checkout", "main")
	git("checkout", "-b", "rename")
	if err := os.MkdirAll(filepath.Join(dir, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "moved.go"), []byte("package moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "source")
	sourceBase := git("rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "internal/moved.go", "docs/moved.md")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "rename")
	renameHead := git("rev-parse", "HEAD")

	s := &Service{}
	fast := func(paths []string, ok bool) ReviewResult {
		return assessFastLand(ReviewResult{Verdict: state.VerdictApproveFast, Summary: "Safe."}, paths, ok)
	}
	docs := fast([]string{"docs/map.md"}, true)
	if docs.Verdict != state.VerdictApproveFast || docs.FastLand == nil || !docs.FastLand.LowRisk || docs.FastLand.Area != state.FastLandDocs {
		t.Fatalf("docs-only fast result = %+v (fast land %+v)", docs, docs.FastLand)
	}
	code := fast([]string{"map.go"}, true)
	if code.Verdict != state.VerdictApprove || code.FastLand == nil || code.FastLand.LowRisk || code.FastLand.Note != "normal path" {
		t.Fatalf("code fast result = %+v (fast land %+v)", code, code.FastLand)
	}
	// A local change has no guarded fast publisher, so it takes the normal path
	// even when its docs are low risk.
	local := s.normalizeReviewResult(context.Background(), Record{Kind: "local", RepoPath: dir, BaseRef: "main", Head: docsHead}, ReviewResult{Verdict: state.VerdictApproveFast, Summary: "Docs."})
	if local.Verdict != state.VerdictApprove || local.FastLand == nil || local.FastLand.Note != "normal path" {
		t.Fatalf("local fast result = %+v (fast land %+v)", local, local.FastLand)
	}
	// A production file moved into docs/ keeps its old path, so it stays risky.
	renamed, err := s.changedPaths(context.Background(), Record{Kind: "local", RepoPath: dir, BaseRef: sourceBase, Head: renameHead})
	if err != nil {
		t.Fatal(err)
	}
	if got := fast(renamed, true); got.Verdict != state.VerdictApprove {
		t.Fatalf("rename into docs counted as docs-only: %v -> %+v", renamed, got)
	}
}

// TestFastLandPublishesWithoutABatch is the merge steward's fast path: a
// low-risk approve-fast lands directly, and the repository starts no trial.
func TestFastLandPublishesWithoutABatch(t *testing.T) {
	e, _, merged := mergeEnv(t, 1)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.Verdict = state.VerdictApproveFast
		r.FastLand = &state.FastLand{LowRisk: true, Area: state.FastLandDocs, Note: "fast landing"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.mergeTickLocked(t.Context(), "", nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*merged, []int{1}) {
		t.Fatalf("fast landing merged %v, want [1]", *merged)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MergeTrials) != 0 {
		t.Fatalf("fast landing started a merge trial: %+v", st.MergeTrials)
	}
}

// TestReviewRedactionCoversHistoryAndPayload keeps a credential out of both the
// saved review and the delivered event, with no path for the raw text to reach
// an agent.
func TestReviewRedactionCoversHistoryAndPayload(t *testing.T) {
	e, r := namedReviewEnv(t)
	const secret = "ghp_this_must_never_leave_reviews"
	summary := "Please rotate `" + secret + "` before landing."
	if err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, ReviewResult{Verdict: state.VerdictChanges, Head: r.Head, Summary: summary}); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if strings.Contains(got.Summary, secret) || len(got.ReviewRounds) != 1 || strings.Contains(got.ReviewRounds[0].Summary, secret) {
		t.Fatalf("saved review kept the credential: %+v", got)
	}
	n := e.sent[len(e.sent)-1]
	if n.Review == nil || strings.Contains(n.Review.Summary, secret) || strings.Contains(n.Review.Detail, secret) || strings.Contains(n.Text, secret) {
		t.Fatalf("delivered event kept the credential: %+v", n.Review)
	}
	if !strings.Contains(got.Summary, "[redacted]") {
		t.Fatalf("review was not redacted: %q", got.Summary)
	}
}

// TestStopEarlyEndsReviewUntilANewHeadOrARestart stops the endless review cycle
// a stable too-large or empty result would otherwise cause.
func TestStopEarlyEndsReviewUntilANewHeadOrARestart(t *testing.T) {
	e, r := namedReviewEnv(t)
	if err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, ReviewResult{Verdict: state.VerdictStopEarly, StopReason: state.StopTooLarge, Head: r.Head}); err != nil {
		t.Fatal(err)
	}
	got := e.record()
	if prfacts.NeedsReview(got) {
		t.Fatal("a stopped review still asks for another reviewer")
	}
	if review, _, reason := (ReviewFacts{}).ReviewState(got); review != "blocked" || !strings.Contains(reason, "too-large") {
		t.Fatalf("stopped state = %q, %q", review, reason)
	}
	// A new head is the normal recovery.
	next := got
	next.Head = "a-new-head"
	if !prfacts.NeedsReview(next) {
		t.Fatal("a new head did not resume review")
	}
	// Restart is the explicit recovery on the same head.
	if err := e.s.RestartReviewer(context.Background(), r.RoomID); err != nil {
		t.Fatal(err)
	}
	if restarted := e.record(); restarted.StopReason != "" || !prfacts.NeedsReview(restarted) {
		t.Fatalf("restart did not resume review: %+v", restarted)
	}
}
