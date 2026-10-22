package watch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// Under load, unbounded parallel gh processes starved each other past the
// 20 s request deadline and were killed.
func TestGitHubBoundsConcurrentRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		saved := ghSlots
		ghSlots = make(chan struct{}, cap(saved))
		defer func() { ghSlots = saved }()
		release := make(chan struct{})
		var running atomic.Int32
		g := GitHub{Executable: "gh", run: func(ctx context.Context, cmd *exec.Cmd) error {
			running.Add(1)
			defer running.Add(-1)
			<-release
			return nil
		}}
		for range cap(ghSlots) + 3 {
			go g.command(context.Background(), "", false, "api", "x")
		}
		synctest.Wait()
		if got := running.Load(); got != int32(cap(ghSlots)) {
			t.Fatalf("%d gh requests ran at once, want the bound %d", got, cap(ghSlots))
		}
		close(release)
		synctest.Wait()
		if got := running.Load(); got != 0 {
			t.Fatalf("%d gh requests still running", got)
		}
	})
}

func TestFixedContentUsesCacheWithoutRequest(t *testing.T) {
	var calls atomic.Int32
	g := GitHub{Executable: "gh", run: func(ctx context.Context, cmd *exec.Cmd) error {
		calls.Add(1)
		_, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 304 Not Modified\r\n\r\n")
		return err
	}}
	ref := Ref{Host: "github.com", Repo: "o/r", Number: 1}
	fixed := "repos/o/r/contents/.github/workflows/ci.yml?ref=" + strings.Repeat("a", 40)
	branch := "repos/o/r/contents/.github/workflows/ci.yml?ref=main"
	// A cache without a clock, used or rate map creates what it first needs.
	g.requests = &requestCache{entries: map[string]cachedResponse{
		"github.com/" + fixed:  {ETag: `"e"`, Body: []byte(`{"content":""}`)},
		"github.com/" + branch: {ETag: `"e"`, Body: []byte(`{"content":""}`)},
	}}
	body, _, err := g.request(context.Background(), ref, fixed)
	if err != nil || string(body) != `{"content":""}` || calls.Load() != 0 {
		t.Fatalf("contents at a commit SHA returned %q, %v after %d gh requests, want the cached body without a request", body, err, calls.Load())
	}
	if _, kept := g.requests.used["github.com/"+fixed]; !kept {
		t.Fatal("the cached contents were not kept for the next poll")
	}
	if _, _, err = g.request(context.Background(), ref, branch); err != nil || calls.Load() != 1 {
		t.Fatalf("contents at a branch made %d gh requests (%v), want 1 revalidation", calls.Load(), err)
	}
	if head := responseHead(fixed); head != strings.Repeat("a", 40) {
		t.Fatalf("responseHead(%q) = %q, want the commit so a new head drops the entry", fixed, head)
	}
}
