package watch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/subagent"
)

func closeFakeReviewer(e *testEnv) func(context.Context, string) error {
	return func(_ context.Context, id string) error {
		info := e.reviewers[id]
		info.Status = subagent.StatusClosed
		e.reviewers[id] = info
		return nil
	}
}

func TestIdleVerdictReleasesAttachedReviewer(t *testing.T) {
	e, keys := readyReviewPool(t, 2)
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil }
	st := dispatchReviewPool(t, e)
	first := *st.Records[keys[0]]
	closed := 0
	close := closeFakeReviewer(e)
	e.s.CloseReviewer = func(ctx context.Context, id string) error { closed++; return close(ctx, id) }
	if err := e.s.Store.update(func(st *State) error {
		st.Records[first.Key].Verdict = "approve"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The verdict ended the reviewer's turn, so the pool may close it (D-411).
	e.endReviewerTurn(first.Reviewer)
	st = dispatchReviewPool(t, e)
	if closed != 1 || e.reviewers[first.Reviewer].Status != subagent.StatusClosed {
		t.Fatal("idle reviewer retained the completed round")
	}
	if st.Records[first.Key].Verdict != "approve" || st.Records[first.Key].RoundHead != first.Head {
		t.Fatal("releasing the reviewer lost its verdict")
	}

	if st.Records[keys[1]].Reviewer == "" {
		t.Fatal("queued review did not start")
	}
	launches := len(e.launches)
	dispatchReviewPool(t, e)
	if len(e.launches) != launches {
		t.Fatal("unchanged approved head started another reviewer")
	}
	// A new head can take a fresh reviewer after the next completed round releases capacity.
	if err := e.s.Store.update(func(st *State) error {
		st.Records[keys[1]].Verdict = "approve"
		resetReview(st.Records[first.Key], strings.Repeat("b", 40))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The second verdict also ended its turn, freeing the capped slot (D-411).
	e.endReviewerTurn(st.Records[keys[1]].Reviewer)
	e.setPullHead(17, strings.Repeat("b", 40))
	st = dispatchReviewPool(t, e)
	if got := st.Records[first.Key]; got.Reviewer == "" || got.Reviewer == first.Reviewer || got.RoundHead != got.Head {
		t.Fatal("new head did not get a fresh reviewer")
	}
}

func TestPartialPollStillDispatchesVerifiedHead(t *testing.T) {
	e, keys := readyReviewPool(t, 2)
	e.s.ReviewerProfiles = func() ([]profile.Profile, error) { return []profile.Profile{{Name: "judge", MaxConcurrent: 1}}, nil }
	before := dispatchReviewPool(t, e)
	orphan := before.Records[keys[0]].Reviewer
	info := e.reviewers[orphan]
	info.Status = subagent.StatusClosed
	e.reviewers[orphan] = info
	if err := e.s.Store.update(func(st *State) error {
		st.Records[keys[0]].Polling.Next = e.clock.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.recordTimeout = 3 * time.Second
	e.s.tickTimeout = time.Second
	started := make(chan struct{}, 1)
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		if strings.Contains(strings.Join(cmd.Args, " "), "repos/owner/repo/pulls/18") {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return ctx.Err()
		}
		return base(ctx, cmd)
	}
	done := make(chan error, 1)
	go func() { _, err := e.s.Tick(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow refresh did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("tick error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial poll did not finish")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	fast, slow := st.Records[keys[0]], st.Records[keys[1]]
	if fast.Reviewer == "" || fast.Reviewer == orphan || fast.RoundHead != fast.Head {
		t.Fatal("slow snapshots skipped dispatch for a verified head")
	}
	// The head batch succeeded before the ancillary snapshot timed out.
	// The only review slot belongs to fast, so slow remains eligible in the queue.
	if !headVerified(*slow) || slow.Polling.Error == "" {
		t.Fatal("ancillary timeout lost the verified head or its retry error")
	}
	if slow.Reviewer != "" {
		t.Fatal("partial poll exceeded the one-reviewer limit")
	}
	if state, _, _ := reviewState(t, e, st, *slow); state != "queued" {
		t.Fatalf("verified head with an ancillary error has review state %q, want queued", state)
	}
}

func TestRestartRequeuesVerdictFromEarlierHead(t *testing.T) {
	e, keys := readyReviewPool(t, 1)
	st := dispatchReviewPool(t, e)
	before := *st.Records[keys[0]]
	e.s.CloseReviewer = closeFakeReviewer(e)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records[before.Key]
		r.RoundHead, r.Verdict, r.Summary = "earlier-head", "changes", "Earlier findings"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RestartReviewer(context.Background(), e.author.RoomID); err != nil {
		t.Fatal(err)
	}
	r, err := e.s.Store.Get(before.Key)
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "" || r.Reviewer == "" || r.Reviewer == before.Reviewer || r.RoundHead != r.Head {
		t.Fatalf("restart lost pending review: %+v", r)
	}
}

func TestRefreshAcceptsEarlierHead(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.setCI("success", 1)
	e.write()
	e.track()
	e.tick()
	e.f.Pull.Head.SHA = strings.Repeat("a", 40)
	e.tick()
	r := e.record()
	if r.Head != e.f.Pull.Head.SHA || r.RoundHead != r.Head || r.HeadStale || r.Error != "" {
		t.Fatal("push back to an earlier SHA did not refresh and enter review")
	}
}
