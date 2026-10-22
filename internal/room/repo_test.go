package room

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSanitizeName(t *testing.T) {
	for _, tt := range []struct {
		in, want, err string
	}{
		{in: "dyna-agent", want: "dyna-agent"},
		{in: "Dyna_Agent", want: "dyna-agent"},
		{in: "My  Cool.Repo!!", want: "my-cool-repo"},
		{in: "--lead--trail--", want: "lead-trail"},
		{in: "a__--__b", want: "a-b"},
		{in: "Ünïcode repo", want: "n-code-repo"},
		{in: "123", want: "123"},
		{in: strings.Repeat("a", 70), want: strings.Repeat("a", 63)},
		{in: strings.Repeat("a", 62) + "-bcd", want: strings.Repeat("a", 62)},
		{in: "___", err: "no letters or digits"},
		{in: "", err: "no letters or digits"},
		{in: "Global", err: "reserved"},
		{in: "room", err: "reserved"},
		{in: "parent", err: "reserved"},
	} {
		got, err := SanitizeName(tt.in)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("SanitizeName(%q) = %q, %v; want error %q", tt.in, got, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("SanitizeName(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
		if err := ValidateName(got); err != nil {
			t.Errorf("SanitizeName(%q) = %q is not a valid name: %v", tt.in, got, err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRepo makes a git repo with one commit at <tmp>/<name> and returns its
// canonical path.
func newRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRepoRootMapsWorktreesToMain(t *testing.T) {
	main := newRepo(t, "My_Project")
	sub := filepath.Join(main, "pkg", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "My_Project-feature")
	git(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	for _, path := range []string{main, sub, linked} {
		root, err := RepoRoot(path)
		if err != nil || root != main {
			t.Fatalf("RepoRoot(%s) = %q, %v; want %q", path, root, err, main)
		}
	}
	name, err := RepoRoomName(main)
	if err != nil || name != "my-project" {
		t.Fatalf("RepoRoomName = %q, %v", name, err)
	}
}

func TestRepoRootOutsideGit(t *testing.T) {
	_, err := RepoRoot(t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("RepoRoot(non-git) err = %v, want ErrNotRepo", err)
	}
}

func TestCreateIsIdempotentPerRepo(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	repoA := newRepo(t, "proj")
	first, err := s.Create(CreateInput{Name: "proj", Repo: repoA})
	if err != nil {
		t.Fatal(err)
	}
	if first.Repo != repoA {
		t.Fatalf("Repo = %q, want %q", first.Repo, repoA)
	}

	// Archived after an idle hour; create again revives the same room.
	now = now.Add(2 * time.Hour)
	if !first.Archived(now, false) {
		t.Fatal("room did not archive")
	}
	again, err := s.Create(CreateInput{Name: "proj", Repo: repoA})
	if err != nil || again.ID != first.ID {
		t.Fatalf("second Create = %#v, %v; want room %s", again, err, first.ID)
	}
	if again.Archived(now, false) {
		t.Fatal("reused room is still archived")
	}
	stored, err := s.Resolve("proj")
	if err != nil || !stored.LastActivity.Equal(now) {
		t.Fatalf("stored record = %#v, %v; want activity %v", stored, err, now)
	}
	if active, _ := s.List(false); len(active) != 1 {
		t.Fatalf("List(false) = %#v; want proj", active)
	}

	// Another repo with the same derived name must pick a distinct name.
	repoB := newRepo(t, "proj")
	_, err = s.Create(CreateInput{Name: "proj", Repo: repoB})
	if err == nil || !strings.Contains(err.Error(), repoA) || !strings.Contains(err.Error(), "dyna room create <name>") {
		t.Fatalf("cross-repo collision err = %v", err)
	}
	other, err := s.Create(CreateInput{Name: "proj-b", Repo: repoB})
	if err != nil || other.ID == first.ID {
		t.Fatalf("distinct name Create = %#v, %v", other, err)
	}

	// An explicit room does not merge with a repo room of the same name.
	if _, err := s.Create(CreateInput{Name: "proj"}); err == nil {
		t.Fatal("explicit create over a repo room succeeded")
	}
}

func TestCreateExplicitNameReuses(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	first, err := s.Create(CreateInput{Name: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Create(CreateInput{Name: "notes"})
	if err != nil || again.ID != first.ID {
		t.Fatalf("explicit reuse = %#v, %v", again, err)
	}
	if _, err := s.Create(CreateInput{Name: "notes", Repo: newRepo(t, "notes")}); err == nil {
		t.Fatal("repo create over an explicit room succeeded")
	}
	for _, bad := range []string{"", "global", "room", "Bad Name", "a--b"} {
		if _, err := s.Create(CreateInput{Name: bad}); err == nil {
			t.Errorf("Create(%q) succeeded", bad)
		}
	}
}

func TestCreateReusesRepoAcrossNamesAndWorktrees(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	repo := newRepo(t, "project")
	first, err := s.Create(CreateInput{Name: "custom", Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, repo, "worktree", "add", "-q", "-b", "linked", linked)
	if err := s.SetArchived(first.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{repo, linked} {
		got, err := s.Create(CreateInput{Name: "different", Repo: path})
		if err != nil || got.ID != first.ID || got.Name != "custom" || got.ManualArchive {
			t.Fatalf("Create(%q) = %+v, %v; want revived %s", path, got, err, first.ID)
		}
	}
}

func TestCreateConcurrentNamesUseOneRepoRoom(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	repo := newRepo(t, "project")
	var wg sync.WaitGroup
	ids := make(chan string, 6)
	for i := range 6 {
		wg.Go(func() {
			rec, err := s.Create(CreateInput{Name: fmt.Sprintf("room-%d", i), Repo: repo})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- rec.ID
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent Create returned %s and %s", first, id)
		}
	}
}
