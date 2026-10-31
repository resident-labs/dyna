package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Slow CI/discussion work must not delay confirmation of any tracked head.
func TestAllHeadsVerifiedBeforeSlowSnapshotWork(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	first := e.track()
	keys := []string{first.Key}
	if err := e.s.SetReviewEnabled(first.RoomID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		for i := 1; i < 12; i++ {
			r := *st.Records[first.Key]
			r.Number += i
			r.Key = fmt.Sprintf("github.com/owner/repo#%d", r.Number)
			r.ChangeID = r.Key
			st.Records[r.Key] = &r
			keys = append(keys, r.Key)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.clock = e.clock.Add(time.Minute)
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.write()
	repo := map[string]any{"defaultBranchRef": map[string]string{"name": "main"}}
	for i := 0; i < 12; i++ {
		number := first.Number + i
		repo[fmt.Sprintf("p%d", number)] = map[string]any{
			"number": number, "title": "Changed head", "state": "OPEN", "merged": false,
			"updatedAt": e.clock.Format(time.RFC3339), "headRefOid": e.f.Pull.Head.SHA, "headRefName": "feature",
			"baseRefOid": strings.Repeat("c", 40), "baseRefName": "main", "author": map[string]string{"login": "owner", "__typename": "User"},
			"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]string{"state": "SUCCESS"}}}}},
		}
	}
	batch, _ := json.Marshal(map[string]any{"data": map[string]any{"r0": repo, "rateLimit": map[string]any{"remaining": 4999, "resetAt": e.clock.Add(time.Hour).Format(time.RFC3339)}}})
	base := e.s.GH.run
	e.s.recordTimeout = 30 * time.Second
	e.s.tickTimeout = 90 * time.Second
	if !t.Run("all_heads_before_ancillary_timeout", func(t *testing.T) {
		previous := e.t
		e.t = t
		defer func() { e.t = previous }()
		var batches, heads, extras atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		var enteredOnce, releaseOnce sync.Once
		e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
			for _, arg := range cmd.Args {
				if arg == "graphql" {
					batches.Add(1)
					fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
					_, err := cmd.Stdout.Write(batch)
					return err
				}
				if strings.Contains(arg, "/check-runs?") {
					extras.Add(1)
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-release:
						return context.DeadlineExceeded
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if strings.HasPrefix(arg, "repos/owner/repo/pulls/") && len(strings.Split(arg, "/")) == 5 {
					heads.Add(1)
				}
			}
			return base(ctx, cmd)
		}
		tickCtx, stopTick := context.WithCancel(t.Context())
		done := make(chan struct{})
		var tickErr error
		go func() {
			_, tickErr = e.s.Tick(tickCtx)
			close(done)
		}()
		t.Cleanup(func() {
			releaseOnce.Do(func() { close(release) })
			stopTick()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			select {
			case <-done:
			case <-ctx.Done():
				t.Error("head tick did not stop after releasing snapshot work")
			}
		})
		wait := func(event <-chan struct{}, phase string) {
			t.Helper()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			select {
			case <-event:
			case <-ctx.Done():
				t.Fatalf("%s did not finish", phase)
			}
		}
		wait(entered, "snapshot entry")
		assertHeads := func() {
			t.Helper()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range keys {
				r := st.Records[key]
				if r.Head != e.f.Pull.Head.SHA || r.HeadStale || !r.HeadVerifiedAt.Equal(e.clock) || r.Error != "" {
					t.Fatalf("slow extras prevented head verification for %s: head=%s stale=%v verified=%s error=%q", key, r.Head, r.HeadStale, r.HeadVerifiedAt, r.Error)
				}
				if r.CI == "passed" || landable(st, *r) {
					t.Fatalf("new head inherited old readiness: %s", key)
				}
			}
		}
		// All heads must already be committed while ancillary work is still held.
		assertHeads()
		releaseOnce.Do(func() { close(release) })
		wait(done, "snapshot timeout")
		if !errors.Is(tickErr, context.DeadlineExceeded) {
			t.Fatalf("slow snapshot error=%v", tickErr)
		}
		assertHeads()
		if batches.Load() != 1 || heads.Load() != 0 || extras.Load() == 0 {
			t.Fatalf("calls: batches=%d head REST=%d slow extras=%d", batches.Load(), heads.Load(), extras.Load())
		}
		if len(e.launches) != 0 {
			t.Fatalf("head-only fixture launched %d reviewers", len(e.launches))
		}
	}) {
		return
	}
	e.s.GH.run = base
	t.Run("ancillary_retry_keeps_verified_heads", func(t *testing.T) {
		previous := e.t
		e.t = t
		defer func() { e.t = previous }()
		e.clock = e.clock.Add(time.Minute)
		retryCtx, stopRetry := context.WithTimeout(t.Context(), 30*time.Second)
		defer stopRetry()
		if _, err := e.s.Tick(retryCtx); err != nil {
			t.Fatal(err)
		}
		st, err := e.s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			r := st.Records[key]
			if r.CI != "passed" || r.Polling.Error != "" || !headVerified(*r) {
				t.Fatalf("ancillary retry failed: %s CI=%s error=%q", key, r.CI, r.Polling.Error)
			}
		}
	})
}

func TestHeadBatchPartialFailureDoesNotBlockOtherHeads(t *testing.T) {
	e := newEnv(t)
	first := e.track()
	second := Ref{"github.com", "owner/repo", 18}
	if err := e.s.Store.update(func(st *State) error {
		copy := *st.Records[first.Key]
		copy.Key, copy.Number = second.Key(), second.Number
		st.Records[copy.Key] = &copy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.f.Fail = "/pulls/18"
	e.write()
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		err := base(ctx, cmd)
		for _, arg := range cmd.Args {
			if arg == "graphql" && err == nil {
				return errors.New("partial GraphQL result")
			}
		}
		return err
	}
	if _, err := e.s.Tick(context.Background()); err == nil {
		t.Fatal("missing failed head error")
	}
	good, _ := e.s.Store.Get(first.Key)
	bad, _ := e.s.Store.Get(second.Key())
	if good.Head != e.f.Pull.Head.SHA || !headVerified(good) {
		t.Fatalf("successful head lost: %+v", good)
	}
	if bad.Head != first.Head || !bad.HeadStale || bad.Error == "" {
		t.Fatalf("failed head trusted: %+v", bad)
	}
	e.f.Fail = ""
	e.s.GH.run = base
	e.write()
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad, _ = e.s.Store.Get(second.Key())
	if bad.Head != e.f.Pull.Head.SHA || !headVerified(bad) {
		t.Fatalf("retry did not verify head: %+v", bad)
	}
}

func TestHeadBatchNoticesSameHeadCIRerun(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.setCI("success", 1)
	e.track()
	e.tick()
	if e.record().CI != "passed" {
		t.Fatal("initial CI not passed")
	}
	e.setCI("failure", 2)
	e.tick()
	r := e.record()
	if r.CI != "failed" || r.Polling.CIVersion != "FAILURE" || !headVerified(r) {
		t.Fatalf("same-head rerun missed: %+v", r)
	}
}

// A slow discussion request must not block landing on a confirmed, approved head.
func TestConfirmedHeadRemainsAuthorizedAfterAncillaryTimeout(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	r := e.track()
	e.tick()
	if err := e.s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		live.Verdict, live.RoundHead, live.RepoPath = "approve", live.Head, t.TempDir()
		live.Polling.FullChecked = e.clock.Add(-time.Hour)
		cfg := st.Reviewers[live.RoomID]
		cfg.Disabled = true
		st.Reviewers[live.RoomID] = cfg
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if strings.Contains(arg, "/reviews?") {
				<-ctx.Done()
				return ctx.Err()
			}
		}
		return base(ctx, cmd)
	}
	e.s.recordTimeout = 50 * time.Millisecond
	e.s.tickTimeout = 500 * time.Millisecond
	e.clock = e.clock.Add(time.Minute)
	if _, err := e.s.Tick(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v", err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	current := *st.Records[r.Key]
	if !headVerified(current) || current.Head != r.Head {
		t.Fatalf("ancillary timeout invalidated confirmed head: stale=%v error=%q", current.HeadStale, current.Error)
	}

}

// The head batch runs every minute. A head it already confirmed leaves the
// record unchanged, so the batch does not rewrite every watched PR.
func TestHeadBatchKeepsConfirmedHeadUnchanged(t *testing.T) {
	e := newEnv(t)
	e.setCI("success", 1)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.write()
	e.track()
	e.tick()
	before := e.record()
	e.tick()
	if after := e.record(); !after.HeadVerifiedAt.Equal(before.HeadVerifiedAt) {
		t.Fatalf("a confirmed head was stamped again: %s -> %s", before.HeadVerifiedAt, after.HeadVerifiedAt)
	}
	e.f.Pull.Head.SHA = strings.Repeat("b", 40)
	e.tick()
	if after := e.record(); after.Head != e.f.Pull.Head.SHA || !after.HeadVerifiedAt.Equal(e.clock) {
		t.Fatalf("a new head was not confirmed: head %s, verified %s, want %s", after.Head, after.HeadVerifiedAt, e.clock)
	}
}
