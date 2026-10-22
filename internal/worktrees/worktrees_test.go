package worktrees

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRegistryRoundTripAndPathGate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	rec := Record{Path: wt, RunID: "wf_registry", AgentID: 3, Label: "build:a", Profile: "builder", Repo: "/repo", Base: "base", Head: "head", Status: "?? retained", RetainedAt: time.Unix(123, 0).UTC()}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	db, err := registryDB()
	if err != nil {
		t.Fatal(err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM worktree_records WHERE path=?`, wt).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"retainedAt"`) || strings.Contains(body, `"keptAt"`) {
		t.Fatalf("registry timestamp field = %s", body)
	}
	got, err := Get(filepath.Base(wt))
	if err != nil || got.Port == 0 {
		t.Fatalf("Get() = %#v, %v; want an allocated port", got, err)
	}
	rec.Port = got.Port
	if err != nil || !reflect.DeepEqual(got, rec) {
		t.Fatalf("Get() = %#v, %v; want %#v", got, err, rec)
	}
	byPath, err := Get(wt)
	if err != nil || !reflect.DeepEqual(byPath, rec) {
		t.Fatalf("Get(path) = %#v, %v", byPath, err)
	}
	list, err := List()
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], rec) {
		t.Fatalf("List() = %#v, %v", list, err)
	}
	forRun, err := ForRun(rec.RunID)
	if err != nil || len(forRun) != 1 || forRun[0].Path != wt {
		t.Fatalf("ForRun() = %#v, %v", forRun, err)
	}
	if err := MarkRemoved(wt); err != nil {
		t.Fatal(err)
	}
	if list, err := List(); err != nil || len(list) != 0 {
		t.Fatalf("List() after removal = %#v, %v", list, err)
	}

	for _, unsafe := range []string{t.TempDir(), filepath.Join(os.TempDir(), "not-dyna"), filepath.Join(os.TempDir(), "dyna-wt-nested", "child")} {
		if err := Track(Record{Path: unsafe}); err == nil {
			t.Fatalf("Track(%q) accepted unsafe path", unsafe)
		}
	}
}

func TestParseDisposition(t *testing.T) {
	for _, value := range []Disposition{DispositionRetain, DispositionRemoveUnchanged, DispositionRemoveAll} {
		if got, err := ParseDisposition(string(value)); err != nil || got != value {
			t.Fatalf("ParseDisposition(%q) = %q, %v", value, got, err)
		}
	}
	for _, retired := range []string{"preserve", "auto", "all"} {
		if _, err := ParseDisposition(retired); err == nil || !strings.Contains(err.Error(), "retain, remove-unchanged, remove-all") {
			t.Fatalf("ParseDisposition(%q) error = %v", retired, err)
		}
	}
}

func TestApplyRunDispositionRemoveUnchangedSkipsUninspectableWorktree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(wt) })
	rec := Record{Path: wt, RunID: "wf_uninspectable", Repo: t.TempDir(), Head: "head", RetainedAt: time.Now()}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	actions, warnings, err := ApplyRunDispositionWithWarnings(rec.RunID, DispositionRemoveUnchanged)
	if err != nil || len(actions) != 1 || actions[0].Action != ActionSkipped || actions[0].State != StateError || len(warnings) != 1 {
		t.Fatalf("ApplyRunDispositionWithWarnings(remove-unchanged) = %#v, %#v, %v", actions, warnings, err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("remove-unchanged removed uninspectable worktree: %v", err)
	}
}

func TestInspectAndRemoveSafety(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initRepo(t)
	wt := addGitWorktree(t, repo)
	head := git(t, wt, "rev-parse", "HEAD")
	rec := Record{Path: wt, RunID: "wf_inspect", Repo: repo, Base: head, Head: head, RetainedAt: time.Now()}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	if state, err := Inspect(rec); err != nil || state != StateUnchanged {
		t.Fatalf("Inspect unchanged = %q, %v", state, err)
	}
	if err := os.WriteFile(filepath.Join(wt, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if state, err := Inspect(rec); err != nil || state != StateModified {
		t.Fatalf("Inspect modified = %q, %v", state, err)
	}
	if err := Remove(rec, false); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Fatalf("Remove(modified, false) = %v", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("safe removal deleted modified tree: %v", err)
	}
	if err := Remove(rec, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("forced worktree still exists: %v", err)
	}
	if _, err := Get(wt); err == nil {
		t.Fatal("forced removal left registry record")
	}
	if state, err := Inspect(rec); err != nil || state != StateMissing {
		t.Fatalf("Inspect missing = %q, %v", state, err)
	}
}

func TestApplyRunDispositionRetainRemoveUnchangedRemoveAll(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initRepo(t)
	makeRecord := func(runID string) Record {
		wt := addGitWorktree(t, repo)
		head := git(t, wt, "rev-parse", "HEAD")
		rec := Record{Path: wt, RunID: runID, Repo: repo, Base: head, Head: head, RetainedAt: time.Now()}
		if err := Track(rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	retained := makeRecord("wf_retain")
	actions, err := ApplyRunDisposition(retained.RunID, DispositionRetain)
	if err != nil || len(actions) != 1 || actions[0].Action != ActionRetained {
		t.Fatalf("retain actions = %#v, %v", actions, err)
	}
	if _, err := os.Stat(retained.Path); err != nil {
		t.Fatalf("retain removed tree: %v", err)
	}

	unchanged := makeRecord("wf_remove-unchanged")
	modified := makeRecord("wf_remove-unchanged")
	if err := os.WriteFile(filepath.Join(modified.Path, "post-retention.txt"), []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err = ApplyRunDisposition("wf_remove-unchanged", DispositionRemoveUnchanged)
	if err != nil || len(actions) != 2 {
		t.Fatalf("remove-unchanged actions = %#v, %v", actions, err)
	}
	if _, err := os.Stat(unchanged.Path); !os.IsNotExist(err) {
		t.Fatalf("remove-unchanged retained unchanged tree: %v", err)
	}
	if _, err := os.Stat(modified.Path); err != nil {
		t.Fatalf("remove-unchanged removed post-retention modification: %v", err)
	}
	if _, err := Get(modified.Path); err != nil {
		t.Fatalf("remove-unchanged dropped modified registry record: %v", err)
	}
	actions, err = ApplyRunDisposition("wf_remove-unchanged", DispositionRemoveAll)
	if err != nil || len(actions) != 1 || !actions[0].Removed {
		t.Fatalf("remove-all actions = %#v, %v", actions, err)
	}
	if _, err := os.Stat(modified.Path); !os.IsNotExist(err) {
		t.Fatalf("remove-all retained modified tree: %v", err)
	}

	if err := Remove(retained, true); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveForceFallsBackWhenSourceRepoIsGone(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initRepo(t)
	wt := addGitWorktree(t, repo)
	head := git(t, wt, "rev-parse", "HEAD")
	rec := Record{Path: wt, RunID: "wf_orphan", Repo: repo, Base: head, Head: head, RetainedAt: time.Now()}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := Remove(rec, false); err == nil {
		t.Fatal("non-forced removal accepted an uninspectable orphan")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("non-forced removal deleted orphan: %v", err)
	}
	if err := Remove(rec, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("forced orphan still exists: %v", err)
	}
}

func TestRemoveMissingWorktreeCleansRegistry(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initRepo(t)
	wt := addGitWorktree(t, repo)
	head := git(t, wt, "rev-parse", "HEAD")
	rec := Record{Path: wt, RunID: "wf_missing", Repo: repo, Base: head, Head: head, RetainedAt: time.Now()}
	if err := Track(rec); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if err := Remove(rec, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(wt); err == nil {
		t.Fatal("missing worktree registry record survived removal")
	}
}

// fixtures are repositories TestMain builds once. Tests copy them, because a
// copy is much cheaper than the git processes that build one.
var fixtures struct {
	base     string // one commit of base.txt
	gc       string // base on main, pushed to gcRemote
	gcRemote string // bare remote of gc
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dyna-worktrees-fixtures-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 1
	if err := buildFixtures(dir); err != nil {
		fmt.Fprintln(os.Stderr, "build fixtures:", err)
	} else {
		code = m.Run()
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

func buildFixtures(dir string) error {
	fixtures.base = filepath.Join(dir, "base")
	fixtures.gc = filepath.Join(dir, "gc")
	fixtures.gcRemote = filepath.Join(dir, "gc-remote")
	if err := os.MkdirAll(fixtures.base, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(fixtures.base, "base.txt"), []byte("base\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"-C", fixtures.base, "init"},
		{"-C", fixtures.base, "config", "user.name", "Dyna Test"},
		{"-C", fixtures.base, "config", "user.email", "dyna-test@example.com"},
		{"-C", fixtures.base, "add", "base.txt"},
		{"-C", fixtures.base, "commit", "-m", "base"},
	} {
		if _, err := runGit(args...); err != nil {
			return err
		}
	}
	// The gc fixture is base plus the commands gcRepo used to run per test.
	if err := os.CopyFS(fixtures.gc, os.DirFS(fixtures.base)); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"-C", fixtures.gc, "branch", "-M", "main"},
		{"init", "--bare", fixtures.gcRemote},
		{"-C", fixtures.gc, "remote", "add", "origin", fixtures.gcRemote},
		{"-C", fixtures.gc, "push", "-u", "origin", "main"},
	} {
		if _, err := runGit(args...); err != nil {
			return err
		}
	}
	return nil
}

// copyFixture copies a fixture repository into a new test folder.
func copyFixture(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	return dst
}

func initRepo(t *testing.T) string {
	t.Helper()
	return copyFixture(t, fixtures.base)
}

func addGitWorktree(t *testing.T, repo string) string {
	t.Helper()
	wt, err := os.MkdirTemp("", "dyna-wt-")
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "worktree", "add", "--detach", wt, "HEAD")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "worktree", "remove", "--force", wt)
		_ = cmd.Run()
		_ = os.RemoveAll(wt)
	})
	return wt
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(append([]string{"-C", dir}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runGit(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}
