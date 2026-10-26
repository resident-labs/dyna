package librarian

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
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
func (f *fakeIssues) FileIssue(roomID string, in room.IssueInput) (room.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := room.Issue{ID: "iss" + string(rune('a'+len(f.open))), Title: in.Title, File: in.File, Line: in.Line, Filer: in.Filer, State: "open"}
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

	for i := 0; i < 10; i++ {
		title := fmt.Sprintf("p%d", i)
		issue, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", good(title))
		if err != nil || issue.File != "agents/run.go" || issue.Line != 3 {
			t.Fatalf("FileSmell(%q) = %+v %v", title, issue, err)
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
	for i := 0; i < 8; i++ {
		if n := race([]string{fmt.Sprintf("p%d", i+1)}); n != 1 {
			t.Fatalf("smell %d filed %d times", i, n)
		}
	}
	if n := race([]string{"Global clock", "Hidden env reads", "Mixed IO policy", "Retry loop twice"}); n != 1 {
		t.Fatalf("the last daily slot was taken %d times", n)
	}
}

// A pass started with --now, or a repository whose library.smellsPerDay is 0,
// files past the default daily cap of 10.
func TestSmellCapIsASettingAndNowRunsHaveNone(t *testing.T) {
	h := setup(t)
	h.svc.Issues = &fakeIssues{}
	ctx := context.Background()
	file := func(i int) error {
		sm := Smell{Title: fmt.Sprintf("smell %d", i), At: []string{"agents/run.go"}, Symbols: []string{"Run"}, Cost: "c", Direction: "d"}
		_, err := h.svc.FileSmell(ctx, authority.Current(), h.repo, "room:librarian", sm)
		return err
	}
	states := librarianstate.Store{DataDir: h.svc.DataDir}
	if _, err := states.Update(h.id, false, func(st *librarianstate.State) error {
		st.SmellDay, st.Smells = h.clock.UTC().Format("2006-01-02"), 10
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := file(1); err == nil || !strings.Contains(err.Error(), "daily cap of 10") {
		t.Fatalf("an automatic pass past the default cap = %v, want a daily cap refusal", err)
	}
	if _, err := states.Update(h.id, false, func(st *librarianstate.State) error { st.SmellUncapped = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := file(2); err != nil {
		t.Fatalf("a --now pass past the cap = %v, want it filed", err)
	}
	if _, err := states.Update(h.id, false, func(st *librarianstate.State) error { st.SmellUncapped = false; return nil }); err != nil {
		t.Fatal(err)
	}
	h.svc.Settings = func() (settings.Values, error) {
		v, err := settings.Load(filepath.Join(t.TempDir(), "absent.json"))
		v.LibrarySmellsPerDay = 0
		return v, err
	}
	if err := file(3); err != nil {
		t.Fatalf("smellsPerDay 0 past ten smells = %v, want no cap", err)
	}
}
