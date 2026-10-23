package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTickUnchangedPRUsesOneCall(t *testing.T) {
	for _, status := range []string{"200", "304"} {
		t.Run(status, func(t *testing.T) {
			e := newEnv(t)
			e.f.Pull.Updated = "2026-10-17T12:00:00Z"
			e.f.Headers = map[string]string{"ETag": `"pull-v1"`}
			e.setCI("success", 1)
			e.write()
			e.track()
			e.tick()
			before := e.record()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			cacheCount := len(st.Responses[before.Key])
			if status == "304" {
				e.f.Raw = map[string]string{"repos/owner/repo/pulls/17": "HTTP/2.0 304 Not Modified\r\n\r\n"}
			}
			if err := os.WriteFile(e.calls, nil, 0600); err != nil {
				t.Fatal(err)
			}
			e.tick()
			calls, err := os.ReadFile(e.calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], "graphql --include --method POST") {
				t.Fatalf("unchanged poll calls = %s", calls)
			}
			// A quiet PR gets no poll job, so the tick never writes its record.
			after := e.record()
			if !after.Checked.Equal(before.Checked) || !after.Polling.Next.Equal(before.Polling.Next) {
				t.Fatalf("a quiet PR was polled and saved: checked %s -> %s, next %s -> %s", before.Checked, after.Checked, before.Polling.Next, after.Polling.Next)
			}
			if after.CI != before.CI || after.CIKey != before.CIKey || after.RoundHead != before.RoundHead {
				t.Fatalf("cheap poll changed settled state: before=%+v after=%+v", before, after)
			}
			st, err = e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Responses[before.Key]) != cacheCount {
				t.Fatalf("cheap poll discarded full response cache: got %d want %d", len(st.Responses[before.Key]), cacheCount)
			}
		})
	}
}

func TestPollSnapshotRefreshesChangedOrUnfinishedWork(t *testing.T) {
	for _, why := range []string{"pending", "head", "updated", "policy", "error", "artifact", "incomplete", "running budget failure"} {
		t.Run(why, func(t *testing.T) {
			e := newEnv(t)
			e.f.Pull.Updated = "2026-10-17T12:00:00Z"
			e.setCI("success", 1)
			e.write()
			r := e.track()
			e.tick()
			r = e.record()
			st, err := e.s.Store.Read()
			if err != nil {
				t.Fatal(err)
			}
			policy := EffectivePolicy(st, r)
			switch why {
			case "pending":
				r.CI = "pending"
			case "head":
				e.f.Pull.Head.SHA = strings.Repeat("b", 40)
			case "updated":
				e.f.Pull.Updated = "2026-10-17T12:01:00Z"
			case "policy":
				policy.CITestMax = "5s"
			case "error":
				r.Error = "retry failed snapshot"
			case "artifact":
				r.CITestRetries = map[string]int{"99/1": 0}
			case "incomplete":
				r.Polling.PullVersion = ""
			case "running budget failure":
				r.CI = "failed"
				r.Checks = []CheckSummary{{State: "pending"}}
			}
			e.write()
			if err := os.WriteFile(e.calls, nil, 0600); err != nil {
				t.Fatal(err)
			}
			gh := e.s.GH
			gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, rates: st.RateLimits, now: e.s.now}
			_, cheap, err := gh.pollSnapshot(context.Background(), r, policy)
			if err != nil || cheap {
				t.Fatalf("cheap=%v error=%v", cheap, err)
			}
			calls, _ := os.ReadFile(e.calls)
			if count := strings.Count(string(calls), "\n"); count != 7 {
				t.Fatalf("full snapshot made %d calls: %s", count, calls)
			}
		})
	}
}

func TestPollDueRecoversLegacyQuietBackoff(t *testing.T) {
	now := time.Now()
	r := &Record{Watching: true, Checked: now.Add(-2 * time.Minute), Polling: Polling{Next: now.Add(3 * time.Minute), IntervalMinutes: 5}}
	if !pollDue(State{}, r, now) {
		t.Fatal("legacy quiet schedule delayed the next poll")
	}
	remaining := 499
	st := State{RateLimits: map[string]RateLimit{(RateLimitScope{Resource: "core"}).Key(): {Remaining: &remaining, Reset: now.Add(time.Hour)}}}
	if pollDue(st, r, now) {
		t.Fatal("reserve backoff was bypassed")
	}
}

func TestTickNewHeadPriorityEndsAfterAttempt(t *testing.T) {
	now := time.Now()
	st := State{Records: map[string]*Record{
		"new": {State: "open", Watching: true, Head: "new", RoundHead: "old", Checked: now, HeadSeen: now.Add(time.Second)},
		"old": {State: "open", Watching: true, Head: "same", RoundHead: "same", Checked: now.Add(-time.Hour)},
	}}
	if got := fmt.Sprint(tickKeys(st)); got != "[new old]" {
		t.Fatal(got)
	}
	st.Records["new"].Checked = st.Records["new"].HeadSeen
	if got := fmt.Sprint(tickKeys(st)); got != "[old new]" {
		t.Fatalf("review backlog kept a polled head at the front: %s", got)
	}
}

func TestTickFortyOneQuietPRsUseOneBatch(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.setCI("success", 1)
	e.write()
	e.track()
	e.tick()
	base := e.record()
	if err := e.s.Store.update(func(st *State) error {
		cfg := st.Reviewers[base.RoomID]
		cfg.Disabled = true
		st.Reviewers[base.RoomID] = cfg
		for i := 1; i <= 41; i++ {
			ref := Ref{"github.com", "owner/repo", i}
			r := base
			r.Key, r.Number, r.Notices = ref.Key(), i, nil
			r.Polling.PullVersion = pullVersion(e.f.Pull, EffectivePolicy(*st, r))
			r.Polling.FullChecked = e.clock.Add(time.Minute)
			st.Records[r.Key] = &r
			body, _ := json.Marshal(e.f.Pull)
			st.Responses[r.Key] = map[string]cachedResponse{ref.Host + "/" + ref.path(""): {ETag: `"quiet"`, Body: body}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	baseRun := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		calls.Add(1)
		return baseRun(ctx, cmd)
	}
	e.clock = e.clock.Add(time.Minute)
	started := time.Now()
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("41 unchanged PRs made %d calls", calls.Load())
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	// The batch confirmed every head, so no quiet PR needs a poll or a save.
	for _, r := range st.Records {
		if !r.Checked.Equal(base.Checked) || r.HeadStale {
			t.Fatalf("PR %d: checked %s, stale %v; want an unchanged quiet record", r.Number, r.Checked, r.HeadStale)
		}
	}
	t.Logf("41 unchanged PRs in one batch: %s", time.Since(started))
}

func TestTickNewCommentOnSettledPRTriggersFullPoll(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.setCI("success", 1)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.write()
	e.track()
	e.tick()
	before := len(e.sent)
	e.addTrustedComment(77, "issue-comments", "Please check this result.", "owner")
	e.f.Pull.Updated = e.clock.Add(time.Minute).Format(time.RFC3339)
	e.tick()
	if len(e.sent) != before+1 || !strings.Contains(e.sent[before].Text, "Please check this result.") {
		t.Fatalf("owner comment did not wake author: %+v", e.sent)
	}
}

func TestFullSafetyPollsAreSpreadWithinTenMinutes(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	slots := map[int]int{}
	for i := 1; i <= 41; i++ {
		r := Record{Key: Ref{"github.com", "owner/repo", i}.Key(), Polling: Polling{FullChecked: now}}
		for minute := 1; minute <= 10; minute++ {
			if fullPollDue(r, now.Add(time.Duration(minute)*time.Minute)) {
				slots[minute]++
				break
			}
			if minute == 10 {
				t.Fatalf("PR %d missed its safety poll", i)
			}
		}
	}
	if len(slots) < 5 {
		t.Fatalf("safety polls clustered: %v", slots)
	}
}

func TestSafetyPollFindsChangesWithoutPullMetadataChange(t *testing.T) {
	for _, kind := range []string{"CI rerun", "comment edit"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			e.setCI("success", 1)
			e.f.Pull.Updated = "2026-10-17T12:00:00Z"
			e.addTrustedComment(77, "issue-comments", "Original comment.", "owner")
			e.write()
			e.track()
			e.tick()
			before := e.record()
			if kind == "CI rerun" {
				e.setCI("failure", 2)
			} else {
				e.f.Comments[0].Body = "Updated comment."
			}
			e.clock = e.clock.Add(10 * time.Minute)
			e.write()
			if _, err := e.s.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			after := e.record()
			if !after.Polling.FullChecked.Equal(e.clock) {
				t.Fatal("safety poll did not run")
			}
			if kind == "CI rerun" && (after.CI != "failed" || after.CIKey == before.CIKey) {
				t.Fatalf("safety poll missed CI rerun: %+v", after)
			}
			if kind == "comment edit" && (len(e.sent) == 0 || !strings.Contains(e.sent[len(e.sent)-1].Text, "Updated comment.")) {
				t.Fatalf("safety poll missed comment edit: %+v", e.sent)
			}
		})
	}
}

func TestPollCompletionDoesNotMissNextMinuteTick(t *testing.T) {
	start := time.Date(2026, 10, 17, 12, 0, 5, 0, time.UTC)
	checked := start.Add(19 * time.Second)
	p := Polling{}
	observe(&p, checked, Snapshot{}, false)
	if !p.Next.Before(start.Add(time.Minute)) {
		t.Fatalf("next fixed tick would skip this record: checked=%s next=%s", checked, p.Next)
	}
	if next := nextPollTime(checked, 5); !next.Equal(checked.Add(5 * time.Minute)) {
		t.Fatalf("reserve schedule changed: %s", next)
	}
}

func TestTickPollsEachMinuteWhenCompletionCrossesBoundary(t *testing.T) {
	e := newEnv(t)
	// Running CI keeps a PR polled every minute; a quiet PR is not polled.
	e.setCI("", 1)
	e.f.Checks[0].Status, e.f.Runs[0].Status = "in_progress", "in_progress"
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.write()
	e.track()
	start := time.Date(2026, 10, 17, 12, 0, 50, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(start.UnixNano())
	e.s.Now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	run := e.s.GH.run
	for tick := 0; tick < 2; tick++ {
		tickStart := start.Add(time.Duration(tick) * time.Minute)
		clock.Store(tickStart.UnixNano())
		e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
			clock.Store(tickStart.Add(20 * time.Second).UnixNano())
			return run(ctx, cmd)
		}
		if _, err := e.s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := e.record()
		if !got.Checked.Equal(tickStart.Add(20 * time.Second)) {
			t.Fatalf("tick %d skipped the poll: checked=%s", tick, got.Checked)
		}
		if got.Polling.Next.After(tickStart.Add(time.Minute)) {
			t.Fatalf("tick %d will miss the next tick: next=%s", tick, got.Polling.Next)
		}
	}
}

func TestTickFairnessWithManySlowRecords(t *testing.T) {
	const records = 40
	s := &Service{Store: &Store{Root: t.TempDir()}}
	clock := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	initial := clock.Add(-time.Hour)
	if err := s.Store.update(func(st *State) error {
		for i := 0; i < records; i++ {
			ref := Ref{"github.com", "owner/repo", i + 1}
			ci := "pending"
			roundHead := ""
			if i >= records-4 {
				ci = "passed"
				roundHead = "abcdef0"
			}
			st.Records[ref.Key()] = &Record{Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number, State: "open", Watching: true, Head: "abcdef0", RoundHead: roundHead, CI: ci, Checked: initial}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Complete one worker batch and cancel the second while it is blocked.
	// Holding the second batch prevents faster workers from starting a third.
	for tick := 1; tick <= 5; tick++ {
		clock = clock.Add(time.Minute)
		ready := make(chan chan struct{}, snapshotWorkers)
		s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
			for _, arg := range cmd.Args {
				if arg == "graphql" {
					data := map[string]any{}
					nodes := map[string]any{}
					for i := 1; i <= records; i++ {
						nodes[fmt.Sprintf("p%d", i)] = map[string]any{"number": i, "state": "OPEN", "headRefOid": "abcdef0"}
					}
					data["r0"] = nodes
					fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n")
					return json.NewEncoder(cmd.Stdout).Encode(map[string]any{"data": data})
				}
			}
			release := make(chan struct{})
			select {
			case ready <- release:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-release:
				return errors.New("slow fake request failed")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := s.Tick(ctx); done <- err }()
		stop := func() {
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Errorf("tick error = %v", err)
			}
		}
		// Keep the second batch blocked until cancellation saves its attempts.
		for batch := 0; batch < 2; batch++ {
			releases := make([]chan struct{}, 0, snapshotWorkers)
			for worker := 0; worker < snapshotWorkers; worker++ {
				select {
				case release := <-ready:
					releases = append(releases, release)
				case err := <-done:
					cancel()
					t.Fatalf("tick ended before accepting batch %d worker %d: %v", batch, worker, err)
				}
			}
			if batch == 0 {
				for _, release := range releases {
					close(release)
				}
			}
		}
		stop()
		st, err := s.Store.Read()
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, r := range st.Records {
			if r.Checked.Equal(clock) {
				checked++
			}
		}
		if checked != 8 {
			t.Fatalf("tick recorded %d attempts, want 8", checked)
		}

	}
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, r := range st.Records {
		if r.Checked.After(initial) {
			checked++
		}
	}
	if checked != records {
		t.Fatalf("only %d of %d records were checked within five ticks", checked, records)
	}

}

// A PR with failed CI does not change until a rerun or a new head, and the
// head batch shows both. It must not get a full poll every minute.
func TestTickFailedCIWaitsForBatchChange(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Updated = "2026-10-17T12:00:00Z"
	e.setCI("failure", 1)
	e.write()
	e.track()
	e.tick()
	if r := e.record(); r.CI != "failed" {
		t.Fatalf("CI = %q, want failed", r.CI)
	}
	calls := func() []string {
		t.Helper()
		b, err := os.ReadFile(e.calls)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.calls, nil, 0600); err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	calls()
	e.tick()
	if got := calls(); len(got) != 1 || !strings.Contains(got[0], "graphql") {
		t.Fatalf("an unchanged failed PR made these calls: %q", got)
	}
	e.setCI("success", 2)
	e.tick()
	if r := e.record(); r.CI != "passed" {
		t.Fatalf("a rerun that passed left CI %q, want passed", r.CI)
	}
}
