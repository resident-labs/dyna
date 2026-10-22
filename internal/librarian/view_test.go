package librarian

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/librarianstate"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/store/storetest"
)

func TestViewListsLiveEntriesStaleAndLint(t *testing.T) {
	storetest.ClearStamp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	commitFile(t, repo, "agents/run.go", "package agents\n")
	data := t.TempDir()
	ctx := context.Background()

	v, err := View(ctx, data, repo)
	if err != nil || v.Enabled || v.Entries == nil || v.Stale == nil || v.Lint == nil || len(v.Entries) != 0 {
		t.Fatalf("empty view = %+v %v", v, err)
	}

	r, err := repoid.Resolve(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	// An empty map still lints: the tracked source file has no owner.
	if v, err = View(ctx, data, repo); err != nil || len(v.Entries) != 0 || len(v.Lint) == 0 || v.Lint[0].Rule != 2 {
		t.Fatalf("empty map lint = %+v %v", v.Lint, err)
	}
	lib := library.Store{DataDir: data, Actor: authority.Current()}
	for _, e := range []library.Entry{
		{ID: "agents", Name: "Agents", Kind: "feature", Summary: "Runs agents.", Owns: []string{"agents/**"}},
		{ID: "old", Name: "Old", Kind: "feature", Summary: "Gone.", Owns: []string{"old/**"}},
	} {
		if _, err := lib.Add(r.ID, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lib.Retire(r.ID, "old", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := (librarianstate.Store{DataDir: data}).Update(r.ID, true, func(s *librarianstate.State) error { s.Main, s.Head = r.Main, "abc"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(settings.DefaultPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.DefaultPath(), []byte(`{"library":{"enabled":["`+r.Main+`"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	v, err = View(ctx, data, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Enabled || v.Head != "abc" || v.Repo != r.Main {
		t.Fatalf("state = %+v", v)
	}
	if len(v.Entries) != 1 || v.Entries[0].ID != "agents" {
		t.Fatalf("entries = %+v", v.Entries)
	}
	if len(v.Stale) != 1 || v.Stale[0] != "agents" {
		t.Fatalf("stale = %v, want the unverified entry", v.Stale)
	}
}
