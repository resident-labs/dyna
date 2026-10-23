package lanesync

import (
	"context"
	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// statusFixture makes a repository whose base commit is its saved export and
// whose integration ref holds one commit on top of it.
func statusFixture(t *testing.T) (svc *Service, repo, base, head string) {
	t.Helper()
	repo = t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "commit", "--allow-empty", "-m", "base")
	base = strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	head = commitOn(t, repo, base, "first")
	git(t, repo, "update-ref", "refs/heads/dyna/integration", head)
	common := strings.TrimSpace(git(t, repo, "rev-parse", "--absolute-git-dir"))
	svc = &Service{Dir: t.TempDir()}
	raw := map[string]any{
		"repositories": map[string]any{repo: map[string]any{"origin": map[string]any{"ahead": 0}}},
		"exports":      map[string]any{common: map[string]any{"commit": base}},
	}
	if err := store.WriteJSON(filepath.Join(svc.Dir, "sync.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(svc.Dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(svc.Dir) })
	return svc, repo, base, head
}

// commitOn adds one empty commit on top of parent.
func commitOn(t *testing.T, repo, parent, title string) string {
	t.Helper()
	tree := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD^{tree}"))
	return strings.TrimSpace(git(t, repo, "commit-tree", tree, "-p", parent, "-m", title))
}

// countGit counts the Git reads a service runs, and fails the ones fail
// picks, with a failure that is not "no such ref".
func countGit(t *testing.T, svc *Service, fail func(args []string) bool) *int {
	t.Helper()
	n := new(int)
	svc.git = func(ctx context.Context, repo string, args ...string) (string, error) {
		*n++
		if fail != nil && fail(args) {
			return "", errors.New("git: read failed")
		}
		return read(ctx, repo, args...)
	}
	return n
}

// TestSyncStatusKeepsItsGitViewUntilRefsMove is the work-count control for the
// web refresh: a second Status with unchanged refs runs no Git process, and a
// moved integration ref runs Git again and reports the new head.
func TestSyncStatusKeepsItsGitViewUntilRefsMove(t *testing.T) {
	svc, repo, _, head := statusFixture(t)
	calls := countGit(t, svc, nil)

	first, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	read := *calls
	if read == 0 || first[repo].Integration.Head != head || len(first[repo].Integration.Commits) != 1 {
		t.Fatalf("first status: %d git calls, %+v", read, first[repo].Integration)
	}
	if _, err := svc.Status(); err != nil {
		t.Fatal(err)
	}
	if got := *calls; got != read {
		t.Fatalf("unchanged refs ran %d more git calls, want 0", got-read)
	}

	next := commitOn(t, repo, head, "second")
	git(t, repo, "update-ref", "refs/heads/dyna/integration", next)
	moved, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if *calls == read {
		t.Fatal("a moved integration ref ran no git call")
	}
	row := moved[repo].Integration
	if row.Head != next || len(row.Commits) != 2 || row.Commits[0].SHA != next {
		t.Fatalf("moved status: %+v", row)
	}
}
