package watch

import (
	"context"
	"os"
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
