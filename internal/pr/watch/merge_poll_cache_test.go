package watch

import (
	"context"
	"os/exec"
	"strings"
	"testing"
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
