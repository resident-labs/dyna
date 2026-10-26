package watch

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTimedOutRefreshBlocksOldHeadUntilRetry(t *testing.T) {
	for _, ready := range []bool{false, true} {
		name := "unassigned"
		if ready {
			name = "approved with queued ready notice"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			r := e.track()
			if err := e.s.Store.update(func(st *State) error {
				live := st.Records[r.Key]
				live.CI, live.Checked = "passed", e.clock
				if ready {
					live.RoundHead, live.Verdict = live.Head, "approve"
					noticeIfLandable(*st, live)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.s.Configure(r.RoomID, Reviewer{Profile: "judge"}); err != nil {
				t.Fatal(err)
			}
			e.f.Pull.Head.SHA = strings.Repeat("b", 40)
			e.write()
			base := e.s.GH.run
			failedRefresh := false
			e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
				if strings.Contains(strings.Join(cmd.Args, " "), "graphql") {
					failedRefresh = true
					return context.DeadlineExceeded
				}
				return base(ctx, cmd)
			}
			// Inject the failed request itself. A short real timeout would also
			// constrain the later successful retry and make it depend on host load.
			_, err := e.s.Tick(context.Background())
			if !failedRefresh || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("refresh attempted=%v, error=%v; want a timed-out request", failedRefresh, err)
			}
			stale := e.record()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if !stale.HeadStale || stale.Error == "" || stale.Head != r.Head || !stale.HeadVerifiedAt.Equal(r.HeadVerifiedAt) {
				t.Fatalf("failed refresh lost old head or error: %+v", stale)
			}
			if policyReviewReady(st, stale) || landable(st, stale) || queueState(st, stale.ChangeID) == "ready" {
				t.Fatal("failed refresh left old head actionable")
			}
			if state, _, _ := reviewState(t, e, st, stale); state != "blocked" {
				t.Fatalf("review state = %q, want blocked", state)
			}
			if len(e.launches) != 0 || len(e.landNotices()) != 0 {
				t.Fatal("failed refresh assigned review or sent a ready notice")
			}
			if stale.Polling.Next.After(e.clock.Add(time.Minute)) {
				t.Fatal("retry missed the next fallback")
			}
			e.s.GH.run = base
			e.tick()
			fresh := e.record()
			if fresh.HeadStale || !fresh.HeadVerifiedAt.After(r.HeadVerifiedAt) || fresh.Head != e.f.Pull.Head.SHA || fresh.Error != "" || fresh.RoundHead != fresh.Head || fresh.Reviewer == "" {
				t.Fatalf("retry did not assign the new head: %+v", fresh)
			}
			if len(e.landNotices()) != 0 {
				t.Fatal("old approval became ready on the new head")
			}
			if err := e.s.Verdict(context.Background(), fresh.Key, fresh.Reviewer, fresh.Head, "approve", "Ready", 0); err != nil {
				t.Fatal(err)
			}
			if len(e.landNotices()) != 1 || !strings.Contains(e.landNotices()[0].Text, fresh.Head) {
				t.Fatal("confirmed new head did not become ready")
			}
		})
	}
}

// An expired tick must persist the failure before a later store wake can dispatch.
func TestTickDeadlineMarksAttemptedHeadStale(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	e.s.recordTimeout = time.Second
	e.s.tickTimeout = 100 * time.Millisecond
	entered := make(chan struct{}, 1)
	e.s.GH.run = func(ctx context.Context, _ *exec.Cmd) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := e.s.Tick(context.Background()); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("tick error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not return")
	}
	got := e.record()
	if !got.HeadStale || got.Error == "" || !got.HeadVerifiedAt.Equal(r.HeadVerifiedAt) {
		t.Fatalf("expired tick lost its failed refresh: %+v", got)
	}
	// No new loop is needed. The next minute fallback can retry this record.
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !pollDue(st, &got, e.clock.Add(time.Minute)) {
		t.Fatal("failed head is not due at the next fallback")
	}
}

func TestSuccessfulRefreshRestoresPendingReadyNotice(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.CI, live.RoundHead, live.Verdict = "passed", live.Head, "approve"
		noticeIfLandable(*st, live)
		live.HeadStale, live.Error = true, "context deadline exceeded"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.flush(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	if len(e.sent) != 0 {
		t.Fatal("stale head sent a notice")
	}
	// The notice stays pending; a successful refresh of the same head restores it.
	e.tick()
	got := e.record()
	if got.HeadStale || got.Error != "" || !got.HeadVerifiedAt.After(r.HeadVerifiedAt) || len(e.landNotices()) != 1 {
		t.Fatalf("confirmed head did not regain readiness: %+v", got)
	}
}

func TestMissingLocalBranchStillDeliversWarning(t *testing.T) {
	e := newEnv(t)
	r, dir := localChange(t, e)
	changeGit(t, dir, "checkout", "main")
	changeGit(t, dir, "branch", "-D", r.Branch)
	// Deferred instructions must not hide the operational warning behind them.
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		notice(live, e.author.ID, "Ready to land", "land:"+r.Head)
		notice(live, "reviewer", "Assignment input: review this head", "review:1:"+r.Head)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshLocal(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	if err := e.s.flush(context.Background(), r.Key); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	live := st.Records[r.Key]
	if live.Error == "" || policyReviewReady(st, *live) {
		t.Fatal("missing branch did not block review")
	}
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "branch is unavailable") {
		t.Fatalf("operational warning was blocked: %+v", e.sent)
	}
	if live.Notices[0].Done || live.Notices[1].Done {
		t.Fatal("stale head delivered a landing or review instruction")
	}
}

func queueState(st State, changeID string) string {
	for _, item := range mergeQueue(st, nil) {
		if item.ChangeID == changeID {
			return item.State
		}
	}
	return ""
}
