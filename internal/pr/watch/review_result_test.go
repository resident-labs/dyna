package watch

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/notify"
	prfacts "dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// TestReviewResultRouting keeps today's recipients and wake rules: a changes
// result wakes the author, and every other result is a quiet room post.
func TestReviewResultRouting(t *testing.T) {
	cases := []struct {
		name       string
		result     ReviewResult
		wantTarget func(Record) string
		wantKind   string
	}{
		{"approve", ReviewResult{Verdict: state.VerdictApprove, Summary: "Looks good."}, func(Record) string { return "" }, "approve"},
		{"changes", ReviewResult{Verdict: state.VerdictChanges, Summary: "Please fix bounds.", Findings: []Finding{{Severity: state.FindingBlocking, File: "a.go", Line: 3, Why: "bounds", Fix: "check"}}}, func(r Record) string { return r.Author }, "changes"},
		{"stop-early", ReviewResult{Verdict: state.VerdictStopEarly, StopReason: state.StopSuperseded}, func(Record) string { return "" }, "stop-early"},
		{"approve-fast", ReviewResult{Verdict: state.VerdictApproveFast, Summary: "Safe."}, func(Record) string { return "" }, "approve-fast"},
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
			if c.result.Verdict == state.VerdictApproveFast && !n.Review.FastLand {
				t.Fatal("approve-fast did not preserve its direct-publication marker")
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

// TestApproveFastCodePublishesWithoutABatch proves the reviewer's own
// approve-fast decision lands a code-only PR with passing exact-head CI.
func TestApproveFastCodePublishesWithoutABatch(t *testing.T) {
	e, _, merged := mergeEnv(t, 1)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records["github.com/owner/repo#1"]
		r.Verdict = state.VerdictApproveFast
		r.FastLand = nil
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
	if review, _, reason := (ReviewFacts{}).ReviewState(got); review != "blocked" || !strings.Contains(reason.Reason, "too-large") {
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

// TestReviewResultPostIsTypedForEveryRecipient keeps the one visible shape of a
// review result: a typed review/result post from the engine on the room feed,
// and the same typed post in a session's DM. A raw text post would leave the
// card unable to draw it.
func TestReviewResultPostIsTypedForEveryRecipient(t *testing.T) {
	t.Run("approval on the room feed", func(t *testing.T) {
		e, r := namedReviewEnv(t)
		e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
		e.s.Deliver = nil
		if err := e.s.RecordReviewResult(context.Background(), r.Key, namedReviewerID, ReviewResult{Verdict: state.VerdictApprove, Head: r.Head, Summary: "Looks good."}); err != nil {
			t.Fatal(err)
		}
		assertTypedReviewPost(t, e, r, e.author.Room)
	})
	t.Run("changes in the author's DM", func(t *testing.T) {
		e, r := namedReviewEnv(t)
		e.s.Notify = notify.New(room.DefaultStore(), subagent.NewManager())
		e.s.Deliver = nil
		recordtest.Write(t, e.s.Notify.Sessions.Store.Root, r.Author, subagent.Meta{
			ID: r.Author, Name: "builder", Room: e.author.Room, RoomID: r.RoomID, Resumable: true, HarnessSession: "builder",
		})
		ev := reviewEventOf(r, ReviewResult{Verdict: state.VerdictChanges, Head: r.Head, Summary: "Fix the bounds."}, "Fix the bounds.")
		if _, err := e.s.deliver(context.Background(), r, Notice{ID: "msg_0123456789abcdef", Kind: "review", Target: r.Author, Review: &ev, Text: "Fix the bounds."}); err != nil {
			t.Fatal(err)
		}
		assertTypedReviewPost(t, e, r, e.author.Room+":builder")
	})
}

// assertTypedReviewPost reads the one review post of a room and checks that it is
// the engine's typed result event addressed to wantTo.
func assertTypedReviewPost(t *testing.T, e *testEnv, r Record, wantTo string) {
	t.Helper()
	posts, err := e.s.Notify.Rooms.Messages(r.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	var found []room.Message
	for _, m := range posts {
		if m.Kind == string(room.FamilyReview) {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("review posts = %d, want one", len(found))
	}
	post := found[0]
	if post.Notice.Kind != string(room.TypeResult) || post.From != e.author.Room {
		t.Fatalf("post = %s/%s from %q, want the typed result from %q", post.Kind, post.Notice.Kind, post.From, e.author.Room)
	}
	if len(post.To) != 1 || post.To[0] != wantTo {
		t.Fatalf("recipients = %v, want %s", post.To, wantTo)
	}
	if ev, ok := post.Review(); !ok || string(ev.Kind) == "" {
		t.Fatalf("payload = %+v, %v", ev, ok)
	}
}
