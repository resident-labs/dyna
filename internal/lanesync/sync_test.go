package lanesync

import (
	"context"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/settings"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return string(b)
}
func TestFetchCountsAndDivergence(t *testing.T) {
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	clone := filepath.Join(dir, "clone")
	git(t, dir, "init", "--bare", "--initial-branch=main", remote)
	git(t, dir, "clone", remote, clone)
	git(t, clone, "commit", "--allow-empty", "-m", "base")
	git(t, clone, "push", "origin", "main")
	s := Service{Dir: filepath.Join(dir, "state")}
	tick := func() {
		t.Helper()
		if e := s.Fetch(context.Background(), []string{clone}); e != nil {
			t.Fatal(e)
		}
	}
	tick()
	git(t, clone, "branch", "dyna/integration")
	git(t, clone, "commit", "--allow-empty", "-m", "remote")
	git(t, clone, "push", "origin", "main")
	git(t, clone, "switch", "dyna/integration")
	git(t, clone, "commit", "--allow-empty", "-m", "local")
	before := git(t, clone, "rev-parse", "HEAD")
	tick()
	state, e := s.Status()
	if e != nil {
		t.Fatal(e)
	}
	got := state[clone].Remotes["origin"]
	if got.Ahead != 1 || got.Behind != 1 {
		t.Fatalf("counts: %+v", got)
	}
	ns := notices.Store{Dir: s.Dir}
	list, _ := ns.List()
	if len(list) != 0 {
		t.Fatalf("behind made notice: %+v", list)
	}
	// Rewrite the remote and check that a repeated tick keeps one event.
	git(t, clone, "push", "--force", "origin", "dyna/integration:main")
	tick()
	list, _ = ns.List()
	if len(list) != 1 {
		t.Fatalf("rewrite notices: %+v", list)
	}
	id, at := list[0].ID, list[0].At
	tick()
	list, _ = ns.List()
	if len(list) != 1 || list[0].ID != id || list[0].At != at {
		t.Fatalf("duplicate event: %+v", list)
	}
	// A later fast-forward ends the rewrite condition.
	git(t, clone, "commit", "--allow-empty", "-m", "forward")
	git(t, clone, "push", "origin", "HEAD:main")
	tick()
	list, _ = ns.List()
	if len(list) != 0 {
		t.Fatalf("notice did not clear: %+v", list)
	}
	if after := git(t, clone, "rev-parse", "HEAD~1"); after != before {
		t.Fatal("sync changed local history")
	}
}
func TestTwoRemoteDisagreement(t *testing.T) {
	dir := t.TempDir()
	clone := filepath.Join(dir, "clone")
	a, b := filepath.Join(dir, "a.git"), filepath.Join(dir, "b.git")
	git(t, dir, "init", "--bare", "--initial-branch=main", a)
	git(t, dir, "init", "--bare", "--initial-branch=main", b)
	git(t, dir, "clone", a, clone)
	git(t, clone, "commit", "--allow-empty", "-m", "base")
	git(t, clone, "push", "origin", "main")
	git(t, clone, "remote", "add", "other", b)
	git(t, clone, "push", "other", "main")
	git(t, clone, "branch", "base")
	git(t, clone, "commit", "--allow-empty", "-m", "a")
	git(t, clone, "push", "origin", "main")
	git(t, clone, "switch", "base")
	git(t, clone, "commit", "--allow-empty", "-m", "b")
	git(t, clone, "push", "other", "HEAD:main")
	s := Service{Dir: filepath.Join(dir, "state")}
	if e := s.Fetch(context.Background(), []string{clone}); e != nil {
		t.Fatal(e)
	}
	ns := notices.Store{Dir: s.Dir}
	list, _ := ns.List()
	if len(list) != 1 {
		t.Fatalf("disagreement: %+v", list)
	}
	git(t, clone, "push", "--force", "other", "refs/remotes/origin/main:main")
	if e := s.Fetch(context.Background(), []string{clone}); e != nil {
		t.Fatal(e)
	}
	list, _ = ns.List()
	for _, n := range list {
		if n.Key == disagreementKey(clone, "origin", "other") {
			t.Fatal("disagreement did not clear")
		}
	}
}

func TestTickReturnsTheNextFetchAndUsesTheDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	remote, clone := filepath.Join(dir, "remote.git"), filepath.Join(dir, "clone")
	git(t, dir, "init", "--bare", "--initial-branch=trunk", remote)
	git(t, dir, "clone", remote, clone)
	git(t, clone, "commit", "--allow-empty", "-m", "base")
	git(t, clone, "push", "origin", "trunk")
	git(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	v, e := settings.Load("")
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal([]string{clone})
	if e := v.Set(settings.KeyLocalLaneRepos, string(data)); e != nil {
		t.Fatal(e)
	}
	if e := v.Save(); e != nil {
		t.Fatal(e)
	}
	s := Service{Dir: filepath.Join(dir, "state")}
	now := time.Now()
	next, e := s.Tick(context.Background(), now)
	if e != nil {
		t.Fatal(e)
	}
	// The engine's timer wakes the next fetch.
	if !next.Equal(now.Add(fetchEvery)) {
		t.Fatalf("next fetch at %v, want %v", next, now.Add(fetchEvery))
	}
	first, _ := s.Status()
	r := first[clone].Remotes["origin"]
	if r.Branch != "refs/remotes/origin/trunk" || r.Ahead != 0 || r.Behind != 0 || r.LastError != "" {
		t.Fatalf("%+v", r)
	}
	git(t, clone, "commit", "--allow-empty", "-m", "next")
	git(t, clone, "push", "origin", "trunk")
	git(t, clone, "update-ref", "refs/heads/trunk", "HEAD^")
	if _, e := s.Tick(context.Background(), now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	latest, _ := s.Status()
	if latest[clone].Remotes["origin"].Behind != 1 {
		t.Fatalf("%+v", latest)
	}
}

func TestFetchErrorKeepsDisagreement(t *testing.T) {
	// The split remains active while one remote is unavailable.
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.git"), filepath.Join(dir, "b.git")
	clone := filepath.Join(dir, "clone")
	git(t, dir, "init", "--bare", "--initial-branch=main", a)
	git(t, dir, "init", "--bare", "--initial-branch=main", b)
	git(t, dir, "clone", a, clone)
	git(t, clone, "commit", "--allow-empty", "-m", "base")
	git(t, clone, "push", "origin", "main")
	git(t, clone, "remote", "add", "other", b)
	git(t, clone, "branch", "base")
	git(t, clone, "commit", "--allow-empty", "-m", "one")
	git(t, clone, "push", "origin", "main")
	git(t, clone, "switch", "base")
	git(t, clone, "commit", "--allow-empty", "-m", "two")
	git(t, clone, "push", "other", "HEAD:main")
	s := Service{Dir: filepath.Join(dir, "state")}
	if e := s.Fetch(context.Background(), []string{clone}); e != nil {
		t.Fatal(e)
	}
	git(t, clone, "remote", "set-url", "other", filepath.Join(dir, "missing.git"))
	if e := s.Fetch(context.Background(), []string{clone}); e != nil {
		t.Fatal(e)
	}
	list, _ := (notices.Store{Dir: s.Dir}).List()
	if len(list) != 1 {
		t.Fatalf("cleared split on error: %+v", list)
	}
	status, _ := s.Status()
	if status[clone].Remotes["other"].LastError == "" {
		t.Fatal("fetch error missing")
	}
}

func TestBadRepositoryDoesNotDiscardOtherStatus(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	git(t, dir, "init", good)
	s := Service{Dir: filepath.Join(dir, "state")}
	missing := filepath.Join(dir, "missing")
	if e := s.Fetch(context.Background(), []string{missing, good}); e == nil {
		t.Fatal("missing repository did not fail")
	}
	state, e := s.Status()
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := state[good]; !ok {
		t.Fatal("lost successful repository status")
	}
}
