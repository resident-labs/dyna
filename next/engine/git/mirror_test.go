package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	fail  bool
}

func (r *fakeRunner) Run(_ context.Context, _ []string, args ...string) error {
	r.calls = append(r.calls, args)
	if r.fail && len(r.calls) == 2 {
		return errors.New("fetch failed")
	}
	return nil
}

type fakeCache struct {
	called bool
	mirror string
	refs   []string
}

func (c *fakeCache) PushCache(_ context.Context, mirror, _, _ string, refs []string) error {
	c.called = true
	c.mirror = mirror
	c.refs = refs
	return nil
}
func TestPrepareFetchesBeforePushingCache(t *testing.T) {
	r := &fakeRunner{}
	c := &fakeCache{}
	dir := t.TempDir()
	cache, err := (Mirror{Data: dir, Token: "secret", Runner: r}).Prepare(context.Background(), c, "/host", "owner/repo", []string{"+refs/heads/main:refs/heads/main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[1][2] != "fetch" || !c.called || c.mirror != filepath.Join(dir, "git/owner/repo.git") || cache != "/host/git/owner/repo.git" {
		t.Fatalf("calls %v, cache %q, host %+v", r.calls, cache, c)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "secret") {
			t.Fatal("token in command arguments")
		}
	}
}
func TestPrepareDoesNotPushWhenFetchFails(t *testing.T) {
	r := &fakeRunner{fail: true}
	c := &fakeCache{}
	_, err := (Mirror{Data: t.TempDir(), Runner: r}).Prepare(context.Background(), c, "/host", "owner/repo", nil)
	if err == nil || c.called {
		t.Fatalf("error %v, cache called %v", err, c.called)
	}
}
