package librarian

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

type fakeIssues struct {
	mu   sync.Mutex
	open []room.Issue
}

func (f *fakeIssues) Issues(string, bool) ([]room.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]room.Issue{}, f.open...), nil
}
func (f *fakeIssues) FileIssue(roomID, title, note, file string, line int, by string) (room.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := room.Issue{ID: "iss" + string(rune('a'+len(f.open))), Title: title, File: file, Line: line, Filer: by, State: "open"}
	f.open = append(f.open, i)
	return i, nil
}

func TestFileSmellChecks(t *testing.T) {
	h := setup(t)
	issues := &fakeIssues{open: []room.Issue{{ID: "old", Number: 7, Title: "Two retry loops in the agent runner", File: "agents/run.go", State: "open"}}}
	h.svc.Issues = issues
	ctx := context.Background()
	good := func(title string) Smell {
		return Smell{Title: title, At: []string{"agents/run.go:3"}, Symbols: []string{"Run"}, Cost: "Every fix lands twice.", Direction: "Share one runner."}
	}
	refuse := func(sm Smell, want string) {
		t.Helper()
		if _, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", sm); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("FileSmell(%+v) = %v, want error with %q", sm, err, want)
		}
	}
	noRefs := good("Global state in run")
	noRefs.At = nil
	refuse(noRefs, "file references")
	for _, at := range []string{"agents/gone.go", ".", "agents", "../outside.go", "/etc/passwd"} {
		bad := good("Global state in run")
		bad.At = []string{at}
		refuse(bad, "file")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "existing.go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(h.repo, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("agents", filepath.Join(h.repo, "alias")); err != nil {
		t.Fatal(err)
	}
	escape := good("Global state in run")
	escape.At = []string{"linked/existing.go"}
	refuse(escape, "inside the repository")
	aliased := good("Agent runner has two retry loops")
	aliased.At = []string{"alias/run.go"}
	refuse(aliased, "open issue D-7")
	dotted := good("Agent runner has two retry loops")
	dotted.At = []string{"./agents//run.go:3"}
	refuse(dotted, "open issue D-7")
	noCost := good("Global state in run")
	noCost.Cost = ""
	refuse(noCost, "--cost")
	refuse(good("two retry loops in the agent runner"), "open issue D-7")
	refuse(good("Agent runner has two retry loops"), "open issue D-7")

	for _, title := range []string{"Global state in run", "Run hides its clock", "Run reads env directly"} {
		i, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", good(title))
		if err != nil || i.File != "agents/run.go" || i.Line != 3 {
			t.Fatalf("FileSmell(%q) = %+v %v", title, i, err)
		}
	}
	refuse(good("Run mixes IO and policy"), "daily cap")
	h.clock = h.clock.AddDate(0, 0, 1)
	if _, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", good("Run mixes IO and policy")); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

// TestConcurrentSmellsTakeOneSlot starts smells together behind a barrier.
// Only one may take the last daily slot, and only one of several identical
// smells may be filed.
func TestConcurrentSmellsTakeOneSlot(t *testing.T) {
	h := setup(t)
	h.svc.Issues = &fakeIssues{}
	ctx := context.Background()
	race := func(titles []string) int {
		var wg sync.WaitGroup
		var mu sync.Mutex
		filed := 0
		start := make(chan struct{})
		for _, title := range titles {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sm := Smell{Title: title, At: []string{"agents/run.go"}, Symbols: []string{"Run"}, Cost: "c", Direction: "d"}
				if _, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", sm); err == nil {
					mu.Lock()
					filed++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		return filed
	}
	if n := race([]string{"Same smell", "Same smell", "Same smell", "Same smell"}); n != 1 {
		t.Fatalf("identical smells filed %d times", n)
	}
	if n := race([]string{"Second smell"}); n != 1 {
		t.Fatalf("second smell filed %d times", n)
	}
	if n := race([]string{"Global clock", "Hidden env reads", "Mixed IO policy", "Retry loop twice"}); n != 1 {
		t.Fatalf("the last daily slot was taken %d times", n)
	}
}
