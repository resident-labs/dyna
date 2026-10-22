package library

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

// gitRepo makes a repository with one commit holding files.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, files)
	commit(t, dir)
	return dir
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func commit(t *testing.T, dir string) string {
	t.Helper()
	run(t, dir, "add", "-A")
	run(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "c")
	return run(t, dir, "rev-parse", "HEAD")
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		glob, name string
		want       bool
	}{
		{"internal/goals/*.go", "internal/goals/a.go", true},
		{"internal/goals/*.go", "internal/goals/sub/a.go", false},
		{"internal/goals/**", "internal/goals/sub/a.go", true},
		{"**/*_test.go", "a/b/c_test.go", true},
		{"**/*_test.go", "c_test.go", true},
		{"web/**/*.tsx", "web/src/App.tsx", true},
		{"README.md", "docs/README.md", false},
	} {
		if got := Match(c.glob, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.glob, c.name, got)
		}
	}
}

func TestSentences(t *testing.T) {
	for s, want := range map[string]int{
		"":                            0,
		"Keeps goals":                 1,
		"Keeps goals. Reads main.go.": 2,
		"One. Two. Three.":            3,
	} {
		if got := Sentences(s); got != want {
			t.Errorf("Sentences(%q) = %d, want %d", s, got, want)
		}
	}
}

func entry(id, parent string) Entry {
	return Entry{ID: id, Name: id, Kind: "feature", Parent: parent, Summary: "Does " + id + ".", Owns: []string{id + "/**"}}
}

func TestStoreValidatesEveryWrite(t *testing.T) {
	s, repo := Store{DataDir: t.TempDir(), Actor: authority.Actor{Role: authority.Owner}}, "r1"
	if _, err := s.Add(repo, entry("harnesses", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(repo, entry("claude-code", "harnesses")); err != nil {
		t.Fatal(err)
	}
	bad := []Entry{
		{ID: "Bad_ID", Name: "x", Kind: "feature", Summary: "X.", Owns: []string{"x"}},
		{ID: "x", Name: "x", Kind: "pattern", Summary: "X.", Owns: []string{"x"}},
		{ID: "x", Name: "x", Kind: "feature", Summary: "One. Two. Three.", Owns: []string{"x"}},
		{ID: "x", Name: "x", Kind: "feature", Summary: "X.", Owns: []string{"/abs"}},
		entry("orphan", "missing"),
		entry("harnesses", ""),
	}
	for _, e := range bad {
		if _, err := s.Add(repo, e); err == nil {
			t.Errorf("Add(%+v) succeeded", e)
		}
	}
	if _, err := s.Update(repo, "harnesses", func(e *Entry) error { e.Parent = "claude-code"; return nil }); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle accepted: %v", err)
	}
	if _, err := s.Get(repo, "orphan"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get missing = %v", err)
	}
	rows, err := s.List(repo, false)
	if err != nil || len(rows) != 2 {
		t.Fatalf("List = %v %v", rows, err)
	}
	if other, _ := s.List("r2", true); len(other) != 0 {
		t.Fatalf("maps leak between repos: %v", other)
	}
}

func TestRetireMovesChildren(t *testing.T) {
	s, repo := Store{DataDir: t.TempDir(), Actor: authority.Actor{Role: authority.Owner}}, "r"
	for _, e := range []Entry{entry("a", ""), entry("b", "a"), entry("c", "b"), entry("d", "")} {
		if _, err := s.Add(repo, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Retire(repo, "b", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Get(repo, "c"); c.Parent != "a" {
		t.Fatalf("child of retired entry has parent %q, want a", c.Parent)
	}
	if _, err := s.Retire(repo, "a", "d"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Get(repo, "c"); c.Parent != "d" {
		t.Fatalf("child has parent %q, want successor d", c.Parent)
	}
	if _, err := s.Add(repo, entry("a", "")); err == nil {
		t.Fatal("a retired id was reused")
	}
	if live, _ := s.List(repo, false); len(live) != 2 {
		t.Fatalf("live = %v", live)
	}
}

func TestChangedAndStale(t *testing.T) {
	dir := gitRepo(t, map[string]string{"a/x.go": "x", "b/y.go": "y"})
	base := run(t, dir, "rev-parse", "HEAD")
	write(t, dir, map[string]string{"a/x.go": "x2", "c/z.go": "z"})
	commit(t, dir)
	a, b := entry("a", ""), entry("b", "")
	a.VerifiedAt, b.VerifiedAt = base, base
	entries := []Entry{a, b, entry("c", "")}
	entries[2].Owns = nil
	ctx := context.Background()
	changes, err := Changed(ctx, dir, base, "HEAD", entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].File != "a/x.go" || len(changes[0].Owners) != 1 || changes[1].File != "c/z.go" || len(changes[1].Owners) != 0 {
		t.Fatalf("changes = %+v", changes)
	}
	stale, err := Stale(ctx, dir, entries)
	if err != nil {
		t.Fatal(err)
	}
	if !stale["a"] || stale["b"] || !stale["c"] {
		t.Fatalf("stale = %v, want a (changed) and c (never verified)", stale)
	}
}

func TestRetireRedirectsRelations(t *testing.T) {
	s, repo := Store{DataDir: t.TempDir(), Actor: authority.Actor{Role: authority.Owner}}, "r"
	a, b, c, d := entry("a", ""), entry("b", ""), entry("c", ""), entry("d", "")
	a.Relates, d.Relates = []string{"b", "c"}, []string{"b"}
	for _, e := range []Entry{b, c, a, d} {
		if _, err := s.Add(repo, e); err != nil {
			t.Fatal(err)
		}
	}
	// With a successor, relations move to it without duplicates or a self-relation.
	if _, err := s.Retire(repo, "b", "c"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(repo, "a"); strings.Join(got.Relates, ",") != "c" {
		t.Fatalf("a relates %v, want [c]", got.Relates)
	}
	if got, _ := s.Get(repo, "d"); strings.Join(got.Relates, ",") != "c" {
		t.Fatalf("d relates %v, want [c]", got.Relates)
	}
	// Without a successor, relations to the retired entry end.
	if _, err := s.Retire(repo, "c", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(repo, "a"); len(got.Relates) != 0 {
		t.Fatalf("a relates %v, want none", got.Relates)
	}
}

func TestDiffNamesKeepExactNames(t *testing.T) {
	dir := gitRepo(t, map[string]string{"keep.go": "k"})
	base := run(t, dir, "rev-parse", "HEAD")
	// A verified entry that owns everything is fresh when nothing changed.
	all := Entry{ID: "all", Owns: []string{"**"}, VerifiedAt: base}
	if stale, err := Stale(context.Background(), dir, []Entry{all}); err != nil || stale["all"] {
		t.Fatalf("unchanged entry is stale: %v %v", stale, err)
	}
	names := []string{"docs/é.md", "docs/line\nbreak.md", " lead space.md"}
	files := map[string]string{}
	for _, n := range names {
		files[n] = "x"
	}
	write(t, dir, files)
	commit(t, dir)
	docs := Entry{ID: "docs", Owns: []string{"docs/**", " lead space.md"}}
	changes, err := Changed(context.Background(), dir, base, "HEAD", []Entry{docs})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != len(names) {
		t.Fatalf("changes = %q", changes)
	}
	for _, ch := range changes {
		if len(ch.Owners) != 1 {
			t.Errorf("%q has owners %v", ch.File, ch.Owners)
		}
	}
}

// A change writes only the rows it changes, so a busy library does not hold the writer lock for the whole map.
func TestChangeWritesOnlyChangedRows(t *testing.T) {
	s, repo := Store{DataDir: t.TempDir(), Actor: authority.Actor{Role: authority.Owner}}, "r"
	for _, e := range []Entry{entry("a", ""), entry("b", ""), entry("c", "")} {
		if _, err := s.Add(repo, e); err != nil {
			t.Fatal(err)
		}
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE library_writes(id TEXT); CREATE TRIGGER lw_i AFTER INSERT ON library_entries BEGIN INSERT INTO library_writes VALUES(new.id); END;
		CREATE TRIGGER lw_u AFTER UPDATE ON library_entries BEGIN INSERT INTO library_writes VALUES(new.id); END;
		CREATE TRIGGER lw_d AFTER DELETE ON library_entries BEGIN INSERT INTO library_writes VALUES(old.id); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(repo, "b", func(e *Entry) error { e.Summary = "Changed."; return nil }); err != nil {
		t.Fatal(err)
	}
	var writes []string
	rows, err := db.Query(`SELECT id FROM library_writes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		writes = append(writes, id)
	}
	if len(writes) != 1 || writes[0] != "b" {
		t.Fatalf("rows written = %v, want only b", writes)
	}
	if got, err := s.Get(repo, "b"); err != nil || got.Summary != "Changed." {
		t.Fatalf("Get b = %+v %v", got, err)
	}
	if _, err := s.Get(repo, "a"); err != nil {
		t.Fatal(err)
	}
}
