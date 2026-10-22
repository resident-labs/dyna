package watch

import (
	"context"
	"errors"
	"testing"

	"dyna-agent/internal/subagent"
)

// A watcher shutdown is not a reviewer failure. After SIGTERM a cancelled
// launch stored "context canceled", left the slot blocked, paused the
// PR's review and alerted the owner.
func TestCancelledDispatchDoesNotBlockReviewer(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.s.dispatchAll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("slotFailed = %v, want context.Canceled", err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if cfg := st.Reviewers[e.author.RoomID]; cfg.LastError != "" {
		t.Fatalf("cancellation stored as a reviewer error: %q", cfg.LastError)
	}
	if r := st.Records[keys[0]]; r.PauseReason != "" || len(r.Notices) != 0 {
		t.Fatalf("cancellation paused or alerted: %q %+v", r.PauseReason, r.Notices)
	}
	if err := e.s.reviewBlocked(e.author.RoomID, "launch failed"); err != nil {
		t.Fatal(err)
	}
	st, _ = e.s.Store.Read()
	if got := st.Reviewers[e.author.RoomID].LastError; got != "launch failed" {
		t.Fatalf("real failure LastError = %q, want launch failed", got)
	}
}

// Slots blocked by the old SIGTERM bug recover even while their attached
// reviewer session is busy, as reviewer-252 was.
func TestDispatchClearsLeftoverCancelledReviewerErrors(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	st := dispatchReviewPool(t, e)
	id := st.Records[keys[0]].Reviewer
	if id == "" {
		t.Fatal("no reviewer assigned")
	}
	info := e.reviewers[id]
	info.Status = subagent.StatusRunning
	e.reviewers[id] = info
	if err := e.s.Store.update(func(st *State) error {
		cfg := st.Reviewers[e.author.RoomID]
		cfg.LastError = "context canceled"
		st.Reviewers[e.author.RoomID] = cfg
		st.Records[keys[0]].PauseReason = "review paused: context canceled"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	if got := st.Reviewers[e.author.RoomID].LastError; got != "" {
		t.Fatalf("slot still blocked: %q", got)
	}
	if got := st.Records[keys[0]].PauseReason; got != "" {
		t.Fatalf("review still paused: %q", got)
	}
}

// A PR merged on GitHub but not yet polled must not take a reviewer.
func TestDispatchSkipsPRThatEndedOnGitHub(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	e.f.Pull.State = "closed"
	e.f.Pull.Merged = true
	e.write()
	st := dispatchReviewPool(t, e)
	if len(e.launches) != 0 || st.Records[keys[0]].Reviewer != "" {
		t.Fatalf("merged PR took a reviewer: launches=%d record=%+v", len(e.launches), st.Records[keys[0]])
	}
}
