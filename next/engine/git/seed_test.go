package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

func runReal(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// localGitHub points the mirror's GitHub URL at a folder of bare repositories, and can make its fetches hang.
type localGitHub struct {
	root    string
	hang    atomic.Bool
	fetches atomic.Int32
}

func (g *localGitHub) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	if len(env) > 0 {
		env = append(env, "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=url."+g.root+"/.insteadOf", "GIT_CONFIG_VALUE_1=https://github.com/")
	}
	if slicesContains(args, "fetch") {
		g.fetches.Add(1)
	}
	if g.hang.Load() && (slicesContains(args, "fetch") || slicesContains(args, "ls-remote")) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return GitRunner{}.Run(ctx, env, args...)
}

func slicesContains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

// gitCache pushes for real, as a host cache would receive it.
type gitCache struct{ t *testing.T }

func (c gitCache) PushCache(ctx context.Context, mirror, cache string, refs []string) error {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(cache); err != nil {
		runReal(c.t, "", "init", "--bare", cache)
	}
	runReal(c.t, "", append([]string{"--git-dir", mirror, "push", "--force", cache}, refs...)...)
	return nil
}

// seedFixture has a mirror that already fetched once, and a remote that moved on after.
type seedFixture struct {
	github     *localGitHub
	clock      *atomic.Int64 // the mirror's clock, in seconds
	mirror     *Mirror
	work       string
	cache      string
	first, tip string
}

func newSeedFixture(t *testing.T) *seedFixture {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	remote := filepath.Join(root, "github", "owner", "repo.git")
	runReal(t, "", "init", work)
	runReal(t, work, "commit", "--allow-empty", "-m", "first")
	first := runReal(t, work, "rev-parse", "HEAD")
	runReal(t, "", "clone", "--bare", work, remote)
	github := &localGitHub{root: filepath.Join(root, "github")}
	data, hostData := filepath.Join(root, "data"), filepath.Join(root, "host")
	clock := &atomic.Int64{}
	mirror := &Mirror{Layout: box.Layout{Data: data, HostData: hostData}, Token: "secret", Runner: github, SeedTimeout: time.Second, Now: func() time.Time { return time.Unix(clock.Load(), 0) }}
	if _, err := mirror.Prepare(context.Background(), gitCache{t}, "owner/repo", "ses_0", true); err != nil {
		t.Fatal(err)
	}
	runReal(t, work, "commit", "--allow-empty", "-m", "second")
	tip := runReal(t, work, "rev-parse", "HEAD")
	runReal(t, work, "push", remote, "HEAD:main")
	clock.Add(60) // the mirror's fetch is a minute old
	return &seedFixture{clock: clock, github: github, mirror: mirror, work: work, cache: filepath.Join(hostData, "git/owner/repo.git"), first: first, tip: tip}
}

func (f *seedFixture) cacheMain(t *testing.T) string {
	t.Helper()
	return runReal(t, "", "--git-dir", f.cache, "rev-parse", "refs/heads/main")
}

func TestSeedStartsAtRemoteTipWhenMirrorIsBehind(t *testing.T) {
	f := newSeedFixture(t)
	item, notice, err := f.mirror.Seed(context.Background(), gitCache{t}, "owner/repo", "ses_1")
	if err != nil || notice != "" || item.Base != "main" {
		t.Fatalf("item %+v, notice %q, error %v", item, notice, err)
	}
	if got := f.cacheMain(t); got != f.tip {
		t.Fatalf("cache main %s, want remote tip %s (mirror was at %s)", got, f.tip, f.first)
	}
}

func TestSeedFallsBackToMirrorWhenRemoteHangs(t *testing.T) {
	f := newSeedFixture(t)
	f.github.hang.Store(true)
	start := time.Now()
	item, notice, err := f.mirror.Seed(context.Background(), gitCache{t}, "owner/repo", "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v with a 1 s bound", elapsed)
	}
	if got := f.cacheMain(t); got != f.first {
		t.Fatalf("cache main %s, want the mirror's tip %s", got, f.first)
	}
	if item.Base != "main" || !strings.Contains(notice, "not refreshed from GitHub at start") || !strings.Contains(notice, "git fetch origin main") {
		t.Fatalf("item %+v, notice %q", item, notice)
	}
}

func TestSeedFallsBackWhenRemoteIsGone(t *testing.T) {
	f := newSeedFixture(t)
	if err := os.RemoveAll(filepath.Join(f.github.root, "owner")); err != nil {
		t.Fatal(err)
	}
	_, notice, err := f.mirror.Seed(context.Background(), gitCache{t}, "owner/repo", "ses_1")
	if err != nil || !strings.Contains(notice, "not refreshed from GitHub at start") {
		t.Fatalf("notice %q, error %v", notice, err)
	}
}

func TestSeedStartsTogetherShareFetches(t *testing.T) {
	f := newSeedFixture(t)
	before := f.github.fetches.Load()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, notice, err := f.mirror.Seed(context.Background(), gitCache{t}, "owner/repo", "ses_1"); err != nil || notice != "" {
				t.Errorf("notice %q, error %v", notice, err)
			}
		}()
	}
	wg.Wait()
	if got := f.github.fetches.Load() - before; got < 1 || got > 2 {
		t.Fatalf("%d fetches for 8 starts", got)
	}
	if got := f.cacheMain(t); got != f.tip {
		t.Fatalf("cache main %s, want %s", got, f.tip)
	}
	// A start right after one that just fetched reuses it.
	before = f.github.fetches.Load()
	if _, _, err := f.mirror.Seed(context.Background(), gitCache{t}, "owner/repo", "ses_2"); err != nil || f.github.fetches.Load() != before {
		t.Fatalf("fetches %d after the burst, error %v", f.github.fetches.Load()-before, err)
	}
}
