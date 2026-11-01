package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

type fakeRunner struct {
	calls  [][]string
	fail   string
	remote string
	head   string
}

func (r *fakeRunner) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	for _, arg := range args {
		if arg == r.fail && r.fail != "" {
			return nil, errors.New("git failed")
		}
	}
	if args[0] == "clone" {
		return nil, os.MkdirAll(args[len(args)-1], 0700)
	}
	if args[0] == "ls-remote" {
		return []byte(r.remote), nil
	}
	if len(args) > 2 && args[2] == "ls-remote" {
		return []byte("ref: refs/heads/new-default\tHEAD\nabc\tHEAD\n"), nil
	}
	if len(args) > 2 && args[2] == "symbolic-ref" {
		if args[3] == "HEAD" {
			r.head = strings.TrimPrefix(args[4], "refs/heads/")
			return nil, nil
		}
		if r.head == "" {
			r.head = "main"
		}
		return []byte(r.head + "\n"), nil
	}
	return nil, nil
}

func TestDeleteArchiveRefUsesLease(t *testing.T) {
	r := &fakeRunner{remote: "abc\trefs/dyna/archive/s\n"}
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: r}
	if err := m.DeleteArchiveRef(context.Background(), "owner/repo", "refs/dyna/archive/s", "abc"); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 || !strings.Contains(strings.Join(r.calls[2], " "), "push --force-with-lease=refs/dyna/archive/s:abc https://github.com/owner/repo.git :refs/dyna/archive/s") {
		t.Fatalf("calls %v", r.calls)
	}
	r.remote = ""
	if err := m.DeleteArchiveRef(context.Background(), "owner/repo", "refs/dyna/archive/s", "abc"); err != nil || len(r.calls) != 4 {
		t.Fatalf("already deleted: %v %v", r.calls, err)
	}
	r.remote = "other\trefs/dyna/archive/s\n"
	if err := m.DeleteArchiveRef(context.Background(), "owner/repo", "refs/dyna/archive/s", "abc"); err == nil {
		t.Fatal("changed ref deleted")
	}
}

type fakeCache struct {
	called bool
	mirror string
	refs   []string
}

func (c *fakeCache) PushCache(_ context.Context, mirror, _ string, refs []string) error {
	c.called = true
	c.mirror = mirror
	c.refs = refs
	return nil
}
func TestPrepareFetchesBeforePushingCache(t *testing.T) {
	r := &fakeRunner{}
	c := &fakeCache{}
	dir := t.TempDir()
	m := &Mirror{Layout: box.Layout{Data: dir, HostData: "/host"}, Token: "secret", Runner: r}
	result, err := m.Prepare(context.Background(), c, "owner/repo", "ses_1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0][0] != "clone" || r.calls[0][1] != "--bare" || !c.called || c.mirror != filepath.Join(dir, "git/owner/repo.git") || result.Cache != "/host/git/owner/repo.git" || result.Base != "main" || len(c.refs) != 1 || c.refs[0] != "+refs/heads/*:refs/heads/*" {
		t.Fatalf("calls %v, repo %+v, host %+v", r.calls, result, c)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "secret") {
			t.Fatal("token in command arguments")
		}
	}
}
func TestPrepareRefreshesDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "git/owner/repo.git"), 0700); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	m := &Mirror{Layout: box.Layout{Data: dir, HostData: "/host"}, Token: "secret", Runner: r}
	result, err := m.Prepare(context.Background(), &fakeCache{}, "owner/repo", "ses_1", true)
	if err != nil || result.Base != "new-default" {
		t.Fatalf("base %q, error %v", result.Base, err)
	}
	var fetched, changed bool
	for _, call := range r.calls {
		args := strings.Join(call, " ")
		if strings.Contains(args, "fetch --prune --no-tags origin +refs/heads/*:refs/heads/*") {
			fetched = true
		}
		if strings.Contains(args, "symbolic-ref HEAD refs/heads/new-default") {
			changed = true
		}
	}
	if !fetched || !changed {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestRefReadsTheRequestedRemoteReference(t *testing.T) {
	r := &fakeRunner{remote: "abc\trefs/dyna/archive/ses_1\n"}
	m := &Mirror{Token: "secret", Runner: r}
	hash, err := m.Ref(context.Background(), "owner/repo", "refs/dyna/archive/ses_1")
	if err != nil || hash != "abc" {
		t.Fatalf("hash %q, error %v", hash, err)
	}
}

func TestPrepareRejectsMissingToken(t *testing.T) {
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Runner: &fakeRunner{}}
	_, err := m.Prepare(context.Background(), &fakeCache{}, "owner/repo", "ses_1", true)
	var missing MissingTokenError
	if !errors.As(err, &missing) {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestPrepareDoesNotPushWhenCloneFails(t *testing.T) {
	r := &fakeRunner{fail: "clone"}
	c := &fakeCache{}
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Token: "secret", Runner: r}
	_, err := m.Prepare(context.Background(), c, "owner/repo", "ses_1", true)
	if err == nil || c.called {
		t.Fatalf("error %v, cache called %v", err, c.called)
	}
}
