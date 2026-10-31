package watch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

func TestGraphQLRateLimitAndConcurrentSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	rates := map[string]RateLimit{}
	calls := 0
	gh := GitHub{run: func(_ context.Context, cmd *exec.Cmd) error {
		calls++
		fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\nRetry-After: %s\r\n\r\n{\"errors\":[{\"type\":\"RATE_LIMITED\",\"message\":\"secondary rate limit\"}]} ", now.Add(3*time.Minute).Format(http.TimeFormat))
		return errors.New("graphql error")
	}}
	gh.requests = &requestCache{rates: rates, now: func() time.Time { return now }}
	ref := Ref{Host: "github.com", Repo: "owner/repo"}
	_, _, err := gh.requestMethod(context.Background(), ref, "graphql", "POST")
	var limited *rateLimitError
	if !errors.As(err, &limited) || !limited.until.Equal(now.Add(3*time.Minute)) {
		t.Fatalf("limit = %v", err)
	}
	_, _, err = gh.requestMethod(context.Background(), ref, "graphql", "POST")
	if !errors.As(err, &limited) || calls != 1 {
		t.Fatalf("pause: calls %d, error %v", calls, err)
	}
	now = now.Add(4 * time.Minute)
	gh.run = func(_ context.Context, cmd *exec.Cmd) error {
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n{\"errors\":[{\"type\":\"FORBIDDEN\",\"message\":\"not allowed\"}]}")
		return errors.New("graphql error")
	}
	_, _, err = gh.requestMethod(context.Background(), ref, "graphql", "POST")
	if err == nil || errors.As(err, &limited) {
		t.Fatalf("ordinary GraphQL error: %v", err)
	}
	now = now.Add(-4 * time.Minute)
	scope := RateLimitScope{Host: ref.Host, Resource: "graphql"}
	previous := rates[scope.Key()]
	previous.PauseObservedAt = previous.ObservedAt.Add(1500 * time.Millisecond)
	st := State{RateLimits: map[string]RateLimit{scope.Key(): previous}}
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): {ObservedAt: previous.ObservedAt.Add(time.Second)}}, now)
	if !st.RateLimits[scope.Key()].PauseUntil.Equal(previous.PauseUntil) {
		t.Fatal("concurrent snapshot erased pause")
	}
	// A new primary quota window does not end a secondary pause.
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): {ObservedAt: previous.ObservedAt.Add(2 * time.Second), Reset: now.Add(time.Hour)}}, now)
	if !st.RateLimits[scope.Key()].PauseUntil.Equal(previous.PauseUntil) {
		t.Fatal("new reset erased secondary pause")
	}
	// A slow limited request may have started before a successful request.
	st.RateLimits[scope.Key()] = RateLimit{ObservedAt: previous.ObservedAt.Add(time.Second)}
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): previous}, now)
	if !st.RateLimits[scope.Key()].PauseUntil.Equal(previous.PauseUntil) {
		t.Fatal("older observation lost active pause")
	}
	applyRateLimitsWithRecovery(&st, map[string]RateLimit{scope.Key(): {ObservedAt: previous.ObservedAt.Add(2 * time.Second)}}, now, scope.Key())
	if !st.RateLimits[scope.Key()].PauseUntil.IsZero() {
		t.Fatal("explicit recovery did not clear pause")
	}
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): previous}, now)
	if !st.RateLimits[scope.Key()].PauseUntil.IsZero() {
		t.Fatal("stale snapshot restored a recovered pause")
	}
	// Another request may report a shorter limit after this request starts.
	delete(rates, scope.Key())
	old := time.Unix(1, 0)
	gh.run = func(_ context.Context, cmd *exec.Cmd) error {
		rates[scope.Key()] = RateLimit{PauseUntil: now.Add(10 * time.Minute), PauseObservedAt: old}
		fmt.Fprint(cmd.Stdout, "HTTP/2.0 403 Forbidden\r\nRetry-After: 60\r\n\r\n{\"message\":\"secondary rate limit\"}")
		return errors.New("limited")
	}
	_, _, err = gh.requestMethod(context.Background(), ref, "graphql", "POST")
	if !errors.As(err, &limited) || !rates[scope.Key()].PauseObservedAt.After(old) {
		t.Fatal("shorter new limit did not update its observation time")
	}

	// Keep the newest limit observation even when another pause lasts longer.
	long := RateLimit{PauseUntil: now.Add(10 * time.Minute), PauseObservedAt: old}
	short := RateLimit{PauseUntil: now.Add(time.Minute), PauseObservedAt: old.Add(2 * time.Second)}
	st.RateLimits[scope.Key()] = long
	applyRateLimits(&st, map[string]RateLimit{scope.Key(): short}, now)
	if got := st.RateLimits[scope.Key()]; !got.PauseUntil.Equal(long.PauseUntil) || !got.PauseObservedAt.Equal(short.PauseObservedAt) {
		t.Fatalf("merged limit = %+v", got)
	}
	e := newEnv(t)
	known := RateLimitScope{Host: "github.com", Account: "fixture", Resource: "graphql"}
	if err := e.s.Store.update(func(st *State) error { st.RateLimits[known.Key()] = short; return nil }); err != nil {
		t.Fatal(err)
	}
	available := 100
	if err := e.s.recoverPolling(RateLimit{Remaining: &available, ObservedAt: old.Add(time.Second)}, known); err != nil {
		t.Fatal(err)
	}
	saved, err := e.s.Store.Read()
	if err != nil || saved.RateLimits[known.Key()].PauseUntil.IsZero() {
		t.Fatal("older recovery erased a newer limit", err)
	}
}
