package watch

import (
	"strings"
	"testing"
)

// A new head must retire the whole reviewer assignment, including its mode,
// and must keep the pause reason visible while CI wakes stay paused.
func TestResetReviewClearsReviewerAndKeepsCIPauseReason(t *testing.T) {
	oldHead, newHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	paused := &Record{State: "open", Watching: true, Head: oldHead, RoundHead: oldHead,
		Reviewer: "dyna:rev", ReviewerName: "rev", ReviewerMode: "session", CIPaused: true,
		PauseReason: "CI wakes paused after 3 consecutive failures; use dyna git pr resume or wait for passing CI"}
	resetReview(paused, newHead)
	if paused.Reviewer != "" || paused.ReviewerName != "" || paused.ReviewerMode != "" {
		t.Fatalf("new head kept a stale reviewer assignment: reviewer=%q name=%q mode=%q", paused.Reviewer, paused.ReviewerName, paused.ReviewerMode)
	}
	if paused.PauseReason == "" {
		t.Fatal("new head cleared the pause reason while CI wakes stay paused")
	}
	if paused.LastReviewer != "dyna:rev" || paused.LastReviewHead != oldHead {
		t.Fatalf("new head forgot the remembered assignment: %q at %q", paused.LastReviewer, paused.LastReviewHead)
	}
	waking := &Record{State: "open", Watching: true, Head: oldHead, RoundHead: oldHead,
		Reviewer: "dyna:rev", ReviewerName: "rev", ReviewerMode: "session", PauseReason: "review paused: waiting"}
	resetReview(waking, newHead)
	if waking.PauseReason != "" {
		t.Fatalf("new head kept pause reason %q while CI wakes are not paused", waking.PauseReason)
	}
}
