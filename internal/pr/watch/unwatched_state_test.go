package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedUnwatched(t *testing.T, e *testEnv) string {
	t.Helper()
	ref := Ref{"github.com", "owner/repo", 17}
	if err := e.s.Store.update(func(st *State) error {
		st.Records[ref.Key()] = &Record{Key: ref.Key(), Host: ref.Host, Repo: ref.Repo, Number: ref.Number,
			State: "open", Head: e.f.Pull.Head.SHA, Watching: false, Reviewer: "preserved-reviewer",
			RoundHead: "preserved-round", Verdict: "approve", CI: "passed", PauseReason: "untracked by the owner or agent",
			Polling: Polling{Next: e.clock.Add(5 * time.Minute)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ref.Key()
}

func unwatchedCalls(t *testing.T, e *testEnv) string {
	t.Helper()
	calls, err := os.ReadFile(e.calls)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(calls)
}

func TestUnwatchedBatchUsesOneHeadQuery(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	if err := e.s.Store.update(func(st *State) error {
		r := st.Records[key]
		r.Polling.Next = e.clock
		other := *r
		other.Number = 18
		other.Key = "github.com/owner/repo#18"
		st.Records[other.Key] = &other
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	batches, rest := 0, 0
	e.s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if arg == "graphql" {
				batches++
				query := strings.Join(cmd.Args, " ")
				if strings.Contains(query, "statusCheckRollup") || strings.Contains(query, "reviewThreads") || strings.Contains(query, "workflows:") {
					t.Errorf("unwatched query fetched CI or discussion: %s", query)
				}
				_, err := fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{"p17":{"number":17,"state":"OPEN","headRefOid":"%s"},"p18":{"number":18,"state":"MERGED","headRefOid":"%s"}}}}`, strings.Repeat("a", 40), strings.Repeat("b", 40))
				return err
			}
			if strings.Contains(arg, "/pulls/") {
				rest++
				return writeFakeJSON(cmd.Stdout, e.f.Pull)
			}
		}
		return fmt.Errorf("unexpected request")
	}
	for _, k := range []string{key, "github.com/owner/repo#18"} {
		if err := e.s.refreshUnwatched(t.Context(), k, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	if rest != 2 || batches != 0 {
		t.Fatalf("baseline: batches=%d REST=%d", batches, rest)
	}
	rest = 0
	if err := e.s.Store.update(func(st *State) error {
		st.Records[key].Polling.Next = e.clock
		st.Records["github.com/owner/repo#18"].Polling.Next = e.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshUnwatchedBatch(t.Context(), st, tickKeys(st), nil); err != nil {
		t.Fatal(err)
	}
	st, err = e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if batches != 1 || rest != 0 || st.Records[key].State != "open" || st.Records["github.com/owner/repo#18"].State != "merged" || !st.Records[key].Polling.Next.Equal(e.clock.Add(time.Hour)) {
		t.Fatalf("batches=%d REST=%d open=%+v merged=%+v", batches, rest, st.Records[key], st.Records["github.com/owner/repo#18"])
	}
}

func TestUnwatchedBatchUsesFreshQuota(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	if err := e.s.Store.update(func(st *State) error {
		st.Records[key].Polling.Next = e.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	keys := tickKeys(old)
	if err := e.s.Store.update(func(st *State) error {
		scope := RateLimitScope{Host: "github.com", Resource: "graphql"}
		st.RateLimits[scope.Key()] = RateLimit{PauseUntil: e.clock.Add(time.Hour), PauseScope: scope}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.GH.run = func(_ context.Context, _ *exec.Cmd) error {
		t.Error("batch ignored newly saved quota pause")
		return nil
	}
	if err := e.s.refreshUnwatchedBatch(t.Context(), old, keys, nil); err != nil {
		t.Fatal(err)
	}
	if !e.record().Polling.Next.Equal(e.clock) {
		t.Fatal("paused watch changed schedule")
	}
}

func TestUnwatchedBatchFallsBackWhenHeadMissing(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	if err := e.s.Store.update(func(st *State) error {
		st.Records[key].Polling.Next = e.clock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var batches, rest int
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if arg == "graphql" {
				batches++
				_, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n"+`{"data":{"r0":{}}}`)
				return err
			}
		}
		rest++
		return writeFakeJSON(cmd.Stdout, e.f.Pull)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshUnwatchedBatch(t.Context(), st, tickKeys(st), nil); err != nil {
		t.Fatal(err)
	}
	if batches != 1 || rest != 1 || !e.record().Polling.Next.Equal(e.clock.Add(time.Hour)) {
		t.Fatalf("batches=%d REST=%d record=%+v", batches, rest, e.record())
	}
}

func TestUnwatchedStateRefreshLifecycle(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	ctx := context.Background()
	// Stopping forces one metadata request despite the old active polling time.
	if err := e.s.refreshUnwatched(ctx, key, true, nil); err != nil {
		t.Fatal(err)
	}
	first := e.record()
	if first.Watching || first.State != "open" || first.Polling.IntervalMinutes != 60 || !first.Polling.Next.Equal(e.clock.Add(time.Hour)) {
		t.Fatalf("unexpected stopped record: %+v", first)
	}
	calls := unwatchedCalls(t, e)
	if strings.Count(calls, "api ") != 1 || !strings.Contains(calls, "repos/owner/repo/pulls/17 --include") {
		t.Fatalf("refresh must use one Pull request: %s", calls)
	}
	// A new service uses the persisted schedule and ETag.
	e.s.GH = GitHub{Executable: e.s.GH.Executable}
	e.clock = e.clock.Add(time.Hour - time.Second)
	if err := e.s.refreshUnwatched(ctx, key, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := unwatchedCalls(t, e); got != calls {
		t.Fatalf("early refresh: %s", got)
	}
	e.clock = e.clock.Add(time.Second)
	if err := e.s.refreshUnwatched(ctx, key, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := unwatchedCalls(t, e); strings.Count(got, "api ") != 2 || !strings.Contains(got, "If-None-Match:") {
		t.Fatalf("hourly refresh did not reuse ETag: %s", got)
	}
	// GitHub merging changes only terminal state and polling metadata.
	e.f.Pull.State, e.f.Pull.Merged = "closed", true
	e.write()
	e.clock = e.clock.Add(time.Hour)
	if err := e.s.refreshUnwatched(ctx, key, false, nil); err != nil {
		t.Fatal(err)
	}
	final := e.record()
	if final.State != "merged" || final.Watching || final.Reviewer != first.Reviewer || final.RoundHead != first.RoundHead || final.Verdict != first.Verdict || final.CI != first.CI || final.PauseReason != first.PauseReason || !reflect.DeepEqual(final.Notices, first.Notices) {
		t.Fatalf("terminal refresh changed watch state: %+v", final)
	}
	if len(e.sent) != 0 || len(e.launches) != 0 {
		t.Fatal("refresh sent notices or launched reviewers")
	}
	calls = unwatchedCalls(t, e)
	e.clock = e.clock.Add(2 * time.Hour)
	if err := e.s.refreshUnwatched(ctx, key, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := unwatchedCalls(t, e); got != calls {
		t.Fatalf("terminal record polled again: %s", got)
	}
}

func TestUnwatchedStateRefreshRateLimit(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	e.f.Raw = map[string]string{"repos/owner/repo/pulls/17": "HTTP/2.0 429 Too Many Requests\r\nRetry-After: 7200\r\n\r\n{\"message\":\"secondary rate limit\"}"}
	e.write()
	if err := e.s.refreshUnwatched(context.Background(), key, true, nil); err == nil {
		t.Fatal("rate limit was not reported")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	r := e.record()
	until := e.clock.Add(2 * time.Hour)
	if r.State != "open" || r.Watching || r.Error == "" || !r.Polling.Next.Equal(until) || !st.RateLimit.PauseUntil.Equal(until) || !r.RateLimit.PauseUntil.Equal(until) {
		t.Fatalf("rate limit not saved: %+v, %+v", r, st.RateLimit)
	}
	calls := unwatchedCalls(t, e)
	if err := e.s.refreshUnwatched(context.Background(), key, true, nil); err != nil {
		t.Fatal(err)
	}
	if got := unwatchedCalls(t, e); got != calls {
		t.Fatalf("forced refresh ignored pause: %s", got)
	}
	e.clock = until
	e.f.Raw = nil
	e.f.Pull.State = "closed"
	e.write()
	if err := e.s.refreshUnwatched(context.Background(), key, false, nil); err != nil {
		t.Fatal(err)
	}
	if r := e.record(); r.State != "closed" || r.Error != "" || !r.RateLimit.PauseUntil.IsZero() {
		t.Fatalf("refresh did not recover: %+v", r)
	}
}

func TestUnwatchedStateRefreshFailureAndExclusions(t *testing.T) {
	e := newEnv(t)
	key := seedUnwatched(t, e)
	e.f.Fail = "/pulls/17"
	e.write()
	if err := e.s.refreshUnwatched(context.Background(), key, true, nil); err == nil {
		t.Fatal("failure was not reported")
	}
	if r := e.record(); r.State != "open" || r.Watching || r.Error == "" || strings.Contains(r.Error, "ghp_") || !r.Polling.Next.Equal(e.clock.Add(time.Hour)) {
		t.Fatalf("unexpected failure record: %+v", r)
	}
	calls := unwatchedCalls(t, e)
	for _, kind := range []string{"watched", "local", "closed"} {
		if err := e.s.Store.update(func(st *State) error {
			r := st.Records[key]
			r.Watching, r.Kind, r.State = kind == "watched", "github", "open"
			if kind == "local" {
				r.Kind = "local"
			}
			if kind == "closed" {
				r.State = "closed"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.s.refreshUnwatched(context.Background(), key, true, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := unwatchedCalls(t, e); got != calls {
		t.Fatalf("ineligible records used GitHub: %s", got)
	}
}
