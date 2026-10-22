package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/repoid"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/subagent"
)

func checkoutGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := repoid.Git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

var checkoutTemplates sync.Map // remote -> *checkoutTemplate

type checkoutTemplate struct {
	once sync.Once
	dir  string
}

// checkoutRepo returns a fresh repository. The repository is built once per
// remote and copied, because three git processes per test dominate its cost.
func checkoutRepo(t *testing.T, remote string) string {
	t.Helper()
	v, _ := checkoutTemplates.LoadOrStore(remote, &checkoutTemplate{})
	tpl := v.(*checkoutTemplate)
	tpl.once.Do(func() {
		dir, err := os.MkdirTemp("", "dyna-checkout-template-")
		if err != nil {
			t.Fatal(err)
		}
		tpl.dir = dir
		checkoutGit(t, dir, "init", "-b", "main")
		checkoutGit(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
		checkoutGit(t, dir, "remote", "add", "origin", remote)
	})
	if tpl.dir == "" {
		t.Fatal("checkout template was not built")
	}
	dir := t.TempDir()
	if out, err := exec.Command("cp", "-a", tpl.dir+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("copy checkout template: %v: %s", err, out)
	}
	return dir
}

func TestResolveCheckoutSources(t *testing.T) {
	for _, source := range []string{"cwd", "subdirectory", "head-worktree", "known-record", "configured-main", "wrong-host", "wrong-repo", "missing"} {
		t.Run(source, func(t *testing.T) {
			e := newEnv(t)
			repo := checkoutRepo(t, "git@github.com:Owner/Repo.git")
			r := Record{Host: "github.com", Repo: "owner/repo", Branch: "feature", Kind: "github"}
			st := State{Records: map[string]*Record{}}
			cwd := t.TempDir()
			want := repo
			switch source {
			case "cwd":
				cwd = repo
			case "subdirectory":
				cwd = filepath.Join(repo, "sub")
				if err := os.Mkdir(cwd, 0700); err != nil {
					t.Fatal(err)
				}
			case "head-worktree":
				want = filepath.Join(t.TempDir(), "feature tree")
				checkoutGit(t, repo, "worktree", "add", "-b", "feature", want)
				r.LookupDir = repo
			case "known-record":
				st.Records["other"] = &Record{RepoPath: repo}
			case "configured-main":
				if err := statewrite.MigrateMirrors(store.DataDir()); err != nil {
					t.Fatal(err)
				}
				h := hooks.New()
				if err := h.Add(context.Background(), repo, hooks.Hook{Name: "test", Cwd: "worktree", Event: hooks.BeforeLand, Command: "true", Timeout: "1m", OnFail: "block"}); err != nil {
					t.Fatal(err)
				}
			case "wrong-host":
				cwd = checkoutRepo(t, "https://example.com/owner/repo.git")
				want = ""
			case "wrong-repo":
				cwd = checkoutRepo(t, "https://github.com/other/repo.git")
				want = ""
			case "missing":
				want = ""
			}
			t.Chdir(cwd)
			e.s.resolveCheckout(context.Background(), st, &r)
			if r.RepoPath != want {
				t.Fatalf("got %q, want %q", r.RepoPath, want)
			}
		})
	}
}

func TestTrackRefreshesCheckoutAndTransferCapturesCaller(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	repo := checkoutRepo(t, "https://github.com/owner/repo.git")
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	updated, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, refRecord(r), e.author, sub)
	if err != nil || created || updated.RepoPath != repo || e.record().RepoPath != repo {
		t.Fatalf("refresh: %+v %v", updated, err)
	}
	if err := e.s.Store.update(func(st *State) error { st.Records[r.Key].RepoPath = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	target := subagent.Info{Meta: subagent.Meta{ID: "ses_20261017-000004-dddddddd", RoomID: e.author.RoomID}}
	if err := e.s.Transfer(context.Background(), r.Key, "author", target, sub); err != nil {
		t.Fatal(err)
	}
	if e.record().RepoPath != repo {
		t.Fatal("transfer lost caller checkout")
	}
}

func TestTrackNewRecordCapturesMatchingCheckout(t *testing.T) {
	e := newEnv(t)
	repo := checkoutRepo(t, "ssh://git@github.com/OWNER/REPO.git")
	r, created, err := e.s.Track(context.Background(), authority.Actor{Role: authority.Owner}, Ref{Host: "github.com", Repo: "owner/repo", Number: 17}, e.author, repo)
	if err != nil || !created || r.RepoPath != repo {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCheckoutErrorNamesRepairCommand(t *testing.T) {
	r := Record{Host: "github.com", Repo: "owner/repo", Number: 17}
	if got := checkoutError(r).Error(); !strings.Contains(got, "dyna git pr track https://github.com/owner/repo/pull/17 --repo <path>") {
		t.Fatal(got)
	}
}
