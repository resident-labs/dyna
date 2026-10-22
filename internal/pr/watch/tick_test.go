package watch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTickSlowSnapshotDoesNotDelayOtherPoll(t *testing.T) {
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
	slowStarted, fastPolled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	// Each ancillary endpoint can be read again during later processing.
	var slowOnce, fastOnce sync.Once
	run := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if arg == "repos/owner/repo/pulls/17/reviews?per_page=100" {
				slowOnce.Do(func() { close(slowStarted) })
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if arg == "repos/owner/repo/pulls/18/reviews?per_page=100" {
				select {
				case <-slowStarted:
				case <-ctx.Done():
					return ctx.Err()
				}
				fastOnce.Do(func() { close(fastPolled) })
			}
		}
		return run(ctx, cmd)
	}
	done := make(chan error, 1)
	go func() { _, err := e.s.Tick(context.Background()); done <- err }()
	select {
	case <-fastPolled:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("the second snapshot waited for the slow record")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := e.s.Store.Get(second.Key())
	if err != nil || got.Checked.IsZero() {
		t.Fatalf("second PR was not processed: %v", err)
	}
}

func TestTickSnapshotDeadlineLeavesTimeForNextTick(t *testing.T) {
	e := newEnv(t)
	e.track()
	e.s.recordTimeout = 25 * time.Millisecond
	e.s.tickTimeout = 100 * time.Millisecond
	e.s.GH.run = func(ctx context.Context, _ *exec.Cmd) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("missing record deadline")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	started := time.Now()
	if _, err := e.s.Tick(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("tick took %s", elapsed)
	}
	r := e.record()
	if r.Error == "" || !r.Polling.Next.Equal(e.clock.Add(time.Minute)) {
		t.Fatalf("missing bounded retry: %+v", r.Polling)
	}
}

func TestTickLimitsNoticeDeliveryPerRecord(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		for _, key := range []string{"old", "other"} {
			r := &Record{Key: key, State: "closed"}
			for i := 0; i < 10; i++ {
				r.Notices = append(r.Notices, Notice{ID: fmt.Sprintf("%s-%d", key, i)})
			}
			st.Records[key] = r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	s.Deliver = func(ctx context.Context, r Record, n Notice) (string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("delivery has no deadline")
		}
		counts[r.Key]++
		return n.ID, nil
	}
	for tick := 1; tick <= 3; tick++ {
		if _, err := s.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"old", "other"} {
			if want := min(10, tick*noticesPerTick); counts[key] != want {
				t.Fatalf("tick %d: %s delivered %d, want %d", tick, key, counts[key], want)
			}
		}
	}
}

func TestTickReportsNoticeFailureOnce(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		st.Records["old"] = &Record{Key: "old", State: "closed", Notices: []Notice{{ID: "bad"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Deliver = func(context.Context, Record, Notice) (string, error) {
		calls++
		return "", errors.New("post exceeds 16 KiB")
	}
	if _, err := s.Tick(context.Background()); err == nil {
		t.Fatal("first failure was not reported")
	}
	if calls != 1 {
		t.Fatalf("first tick attempted %d deliveries", calls)
	}
	if _, err := s.Tick(context.Background()); err != nil {
		t.Fatalf("same failure reported twice: %v", err)
	}
	if calls != 2 {
		t.Fatalf("second tick attempted %d total deliveries", calls)
	}
}

func TestTickOrdersNewHeadsThenOldestPollBeforeBacklogs(t *testing.T) {
	now := time.Now()
	st := State{Records: map[string]*Record{
		"a-backlog":   {State: "closed"},
		"b-unwatched": {State: "open"},
		"c-quiet":     {Watching: true, State: "open", Head: "old", RoundHead: "old", CI: "passed", Checked: now.Add(-time.Hour), Polling: Polling{PullHead: "old"}},
		"d-pending":   {Watching: true, State: "open", Head: "old", RoundHead: "old", CI: "pending", Checked: now, Polling: Polling{PullHead: "old"}},
		"e-head":      {Watching: true, State: "open", Head: "new", RoundHead: "old", Checked: now, HeadSeen: now.Add(time.Second)},
		"f-first":     {Watching: true, State: "open", Head: "new"},
	}}
	got := tickKeys(st)
	want := "f-first,e-head,c-quiet,d-pending,a-backlog,b-unwatched"
	if strings.Join(got, ",") != want {
		t.Fatalf("order = %v, want %s", got, want)
	}
}

func TestConcurrentRequestsKeepRatePauseAndSeparateCaches(t *testing.T) {
	now := time.Now()
	rates := map[string]RateLimit{}
	var mu sync.Mutex
	limited := make(chan struct{})
	gh := GitHub{now: func() time.Time { return now }, run: func(ctx context.Context, cmd *exec.Cmd) error {
		path := strings.Join(cmd.Args, " ")
		if strings.Contains(path, "limited") {
			fmt.Fprintf(cmd.Stdout, "HTTP/2.0 429 Too Many Requests\r\nRetry-After: 60\r\nX-Ratelimit-Remaining: 100\r\nX-Ratelimit-Reset: %d\r\n\r\n{}", now.Add(time.Hour).Unix())
			return nil
		}
		// Both requests started before the limit response. Finish success last.
		select {
		case <-limited:
		case <-ctx.Done():
			return ctx.Err()
		}
		fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\nETag: test\r\nX-Ratelimit-Remaining: 900\r\nX-Ratelimit-Reset: %d\r\n\r\n{}", now.Add(time.Hour).Unix())
		return nil
	}}
	success := gh
	success.requests = &requestCache{used: map[string]cachedResponse{}, rates: rates, rateMu: &mu, now: gh.now}
	gh.requests = &requestCache{used: map[string]cachedResponse{}, rates: rates, rateMu: &mu, now: gh.now}
	// Ensure the success request reaches the command before the pause is set.
	entered := make(chan struct{})
	run := success.run
	success.run = func(ctx context.Context, cmd *exec.Cmd) error { close(entered); return run(ctx, cmd) }
	done := make(chan error, 1)
	go func() {
		_, _, err := success.request(context.Background(), Ref{Host: "github.com"}, "success")
		done <- err
	}()
	<-entered
	if _, _, err := gh.request(context.Background(), Ref{Host: "github.com"}, "limited"); err == nil {
		t.Fatal("rate limit was not returned")
	}
	close(limited)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rate := rates[(RateLimitScope{Host: "github.com", Resource: "core"}).Key()]
	if !rate.PauseUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("success cleared the pause: %+v", rate)
	}
	if rate.Remaining == nil || *rate.Remaining != 100 {
		t.Fatalf("out-of-order success raised the quota: %+v", rate)
	}
	if len(success.requests.used) != 1 || len(gh.requests.used) != 0 {
		t.Fatal("workers shared response caches")
	}
}

func TestTickPollsBeforeBacklogDelivery(t *testing.T) {
	e := newEnv(t)
	e.track()
	if err := e.s.Store.update(func(st *State) error {
		st.Records["000-backlog"] = &Record{Key: "000-backlog", State: "closed", Notices: []Notice{{ID: "backlog"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.Deliver = func(_ context.Context, _ Record, n Notice) (string, error) {
		if e.record().Checked.IsZero() {
			t.Fatal("backlog delivery ran before the active poll")
		}
		return n.ID, nil
	}
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTickCancellationStopsQueuedSnapshots(t *testing.T) {
	e := newEnv(t)
	first := e.track()
	if err := e.s.Store.update(func(st *State) error {
		for number := 18; number < 29; number++ {
			r := *st.Records[first.Key]
			ref := Ref{"github.com", "owner/repo", number}
			r.Key, r.Number = ref.Key(), number
			st.Records[r.Key] = &r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	ready := make(chan struct{})
	base := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if arg == "graphql" {
				return base(ctx, cmd)
			}
		}
		if calls.Add(1) == snapshotWorkers {
			close(ready)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := e.s.Tick(ctx); done <- err }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("snapshot workers did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Tick = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != snapshotWorkers {
		t.Fatalf("started %d requests after cancellation; want %d", got, snapshotWorkers)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	attempted := 0
	for key, r := range st.Records {
		if r.Polling.Error != "" {
			attempted++
			if r.Checked.IsZero() || !strings.Contains(r.Polling.Error, context.Canceled.Error()) {
				t.Fatalf("attempted refresh lost cancellation: %s", key)
			}
		} else if !r.Checked.IsZero() || r.Error != "" {
			t.Fatalf("changed an unattempted record: %s", key)
		}
		if r.Head != first.Head || r.HeadStale {
			t.Fatalf("canceled refresh changed head: %s", key)
		}
	}
	if attempted != snapshotWorkers {
		t.Fatalf("saved %d ancillary errors; want %d", attempted, snapshotWorkers)
	}
}

func TestTickCancellationPreservesEarlierError(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		for _, key := range []string{"a-fails", "b-cancels", "c-skipped"} {
			st.Records[key] = &Record{Key: key, State: "closed", Notices: []Notice{{ID: key}}}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deliveryErr := errors.New("delivery failed before cancellation")
	var delivered []string
	s.Deliver = func(_ context.Context, r Record, n Notice) (string, error) {
		delivered = append(delivered, r.Key)
		if r.Key == "a-fails" {
			return "", deliveryErr
		}
		cancel()
		return n.ID, nil
	}
	_, err := s.Tick(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, deliveryErr) {
		t.Fatalf("Tick lost cancellation or earlier failure: %v", err)
	}
	if got := strings.Join(delivered, ","); got != "a-fails,b-cancels" {
		t.Fatalf("delivered after cancellation: %s", got)
	}
}
