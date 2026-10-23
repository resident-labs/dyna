package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The merge queue re-reads every ready PR on each poll. A reply that did not
// change must come back as a 304, which costs no quota, so the second read
// sends the saved validator for its CI and comment paths. Only the pull body
// stays a full read, so a recomputed mergeable verdict is never stale.
func TestMergeReadyRevalidatesSavedResponses(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	expected, err := e.s.Store.Get("github.com/owner/repo#1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ready, err := e.s.mergeReady(t.Context(), expected); err != nil || !ready {
		t.Fatalf("first ready read: ready=%v error=%v", ready, err)
	}
	run := e.s.GH.run
	var calls []string
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		calls = append(calls, strings.Join(cmd.Args[1:], " "))
		return run(ctx, cmd)
	}
	current, ready, err := e.s.mergeReady(t.Context(), expected)
	if err != nil || !ready || current.Head != expected.Head {
		t.Fatalf("second ready read: ready=%v head=%s error=%v", ready, current.Head, err)
	}
	reads, fresh := 0, 0
	for _, call := range calls {
		if !strings.HasPrefix(call, "api ") || strings.Contains(call, "graphql") {
			continue
		}
		reads++
		if !strings.Contains(call, "If-None-Match") {
			fresh++
			if !strings.HasSuffix(call, "pulls/1 --include") {
				t.Fatalf("uncached merge readiness read: %s", call)
			}
		}
	}
	if reads == 0 {
		t.Fatal("second merge readiness read made no API request")
	}
	if fresh != 1 {
		t.Fatalf("%d of %d merge readiness reads were unconditional, want only the pull body", fresh, reads)
	}
}

// A rate limit on the first read of a merge check saves no response, but it
// still proves the pause. Losing it would let the next merge check send the
// same blocked request again.
func TestMergeReadyKeepsThePauseWhenTheFirstRequestFails(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	expected, err := e.s.Store.Get("github.com/owner/repo#1")
	if err != nil {
		t.Fatal(err)
	}
	reset := e.clock.Add(time.Hour)
	e.f.Raw[refRecord(expected).path("")] = fmt.Sprintf("HTTP/2.0 403 Forbidden\r\nX-Ratelimit-Remaining: 0\r\nX-Ratelimit-Reset: %d\r\n\r\n{\"message\":\"API rate limit exceeded\"}", reset.Unix())
	e.write()
	if _, _, err := e.s.mergeReady(t.Context(), expected); err == nil {
		t.Fatal("a rate-limited read reported readiness")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	for key, rate := range st.RateLimits {
		if strings.HasSuffix(key, "core") && rate.PauseUntil.After(e.clock) {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("saved quota pauses = %+v, want the paused core scope", st.RateLimits)
	}
	// The saved pause must stop the next check before it sends the request.
	var calls []string
	run := e.s.GH.run
	e.s.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		calls = append(calls, strings.Join(cmd.Args[1:], " "))
		return run(ctx, cmd)
	}
	if _, _, err := e.s.mergeReady(t.Context(), expected); err == nil {
		t.Fatal("a paused merge check reported readiness")
	}
	for _, call := range calls {
		if strings.HasSuffix(call, "pulls/1 --include") {
			t.Fatalf("repeated the blocked request during the pause: %s", call)
		}
	}
}
