package watch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

func TestBlockedPendingVerdictsPreserveDispatchAndPolling(t *testing.T) {
	e, keys := readyReviewPool(t, 5)
	if err := e.s.Store.update(func(st *State) error {
		for _, key := range keys[:4] {
			r := st.Records[key]
			r.RoundHead, r.Reviewer = r.Head, "finished-reviewer"
			r.PendingVerdict = &state.PendingVerdict{Actor: r.Reviewer, Head: r.Head, Verdict: "approve", Summary: "Review complete"}
			// Only replay is due for these records. The unrelated PR needs a normal poll.
			r.Checked = e.clock
			r.Polling.CIVersion = "SUCCESS"
			r.Polling.Next = e.clock.Add(time.Hour)
		}
		st.Records[keys[4]].Checked = e.clock.Add(-time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A long tick and a short record budget keep the shares far apart: the
	// blocked replay ends after its record budget, and the unrelated poll and
	// dispatch then have the rest of the tick, however slow the machine is.
	e.s.recordTimeout = 300 * time.Millisecond
	e.s.tickTimeout = time.Minute
	started := make(chan struct{}, 4)
	var requests atomic.Int32
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		args := strings.Join(cmd.Args, " ")
		for _, number := range []string{"17", "18", "19", "20"} {
			if strings.Contains(args, "repos/owner/repo/pulls/"+number) {
				requests.Add(1)
				started <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return base(ctx, cmd)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.s.Tick(ctx); done <- err }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("tick ended before replay started: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("replay did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("tick error = %v", err)
		}
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatal("tick did not stop its blocked replay")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	unrelated := st.Records[keys[4]]
	if unrelated.Reviewer == "" || unrelated.RoundHead != unrelated.Head {
		t.Fatal("blocked pending verdicts starved unrelated reviewer dispatch")
	}
	if !unrelated.Checked.Equal(e.clock) {
		t.Fatal("blocked pending verdicts starved the unrelated normal poll")
	}
	for _, key := range keys[:4] {
		if r := st.Records[key]; r.PendingVerdict == nil || r.Verdict != "" {
			t.Fatalf("timeout consumed a pending verdict: %s", key)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("replay scheduled more requests after its shared deadline: %d", requests.Load())
	}
}
