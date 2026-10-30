package watch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestLiveReviewerReassignedAfterHeadChange(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	st := dispatchReviewPool(t, e)
	old := *st.Records[keys[0]]
	head := strings.Repeat("b", 40)
	if err := e.s.Store.update(func(st *State) error {
		st.Records[old.Key].RequiredReviewers = []string{old.Reviewer}
		resetReview(st.Records[old.Key], head)
		st.Records[old.Key].Polling.PullHead = head
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := e.s.Store.Get(old.Key)
	if err != nil {
		t.Fatal(err)
	}
	pending := false
	for _, n := range before.Notices {
		if n.Kind == "required-review" && n.Target == old.Reviewer && !n.Done && n.SupersededBy == "" {
			pending = true
		}
	}
	if !pending {
		t.Fatal("head reset did not queue the required-review notice")
	}
	e.f.Pull.Head.SHA = head
	e.setPullHead(old.Number, head)
	st = dispatchReviewPool(t, e)
	got := st.Records[old.Key]
	if got.Reviewer != old.Reviewer || got.RoundHead != head || len(e.launches) != 1 {
		t.Fatalf("reassignment: reviewer=%s head=%s launches=%d", got.Reviewer, got.RoundHead, len(e.launches))
	}
	woke := false
	for _, n := range e.sent {
		if n.Target == old.Reviewer && strings.Contains(n.Text, head) && strings.Contains(n.Text, "Assignment input:") {
			woke = true
		}
	}
	if !woke {
		t.Fatal("reassigned reviewer was not woken")
	}
	if err := e.s.Verdict(context.Background(), old.Key, old.Reviewer, head, "approve", "looks good", 0); err != nil {
		t.Fatalf("new head verdict: %v", err)
	}
}

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
