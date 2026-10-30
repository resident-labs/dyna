package git

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  [][]string
	fail   string
	remote string
}

func (r *fakeRunner) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	for _, arg := range args {
		if arg == r.fail && r.fail != "" {
			return nil, errors.New("git failed")
		}
	}
	if args[0] == "ls-remote" {
		return []byte(r.remote), nil
	}
	if len(args) > 2 && args[2] == "symbolic-ref" {
		return []byte("main\n"), nil
	}
	return nil, nil
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
	m := &Mirror{Data: dir, Token: "secret", Runner: r}
	result, err := m.Prepare(context.Background(), c, "/host", "owner/repo", "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 2 || r.calls[0][0] != "clone" || !c.called || c.mirror != filepath.Join(dir, "git/owner/repo.git") || result.Cache != "/host/git/owner/repo.git" || result.Base != "main" || len(c.refs) != 1 || c.refs[0] != "+refs/heads/*:refs/heads/*" {
		t.Fatalf("calls %v, repo %+v, host %+v", r.calls, result, c)
	}
	for _, call := range r.calls {
		if strings.Contains(strings.Join(call, " "), "secret") {
			t.Fatal("token in command arguments")
		}
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
	m := &Mirror{Data: t.TempDir(), Runner: &fakeRunner{}}
	_, err := m.Prepare(context.Background(), &fakeCache{}, "/host", "owner/repo", "ses_1")
	var missing MissingTokenError
	if !errors.As(err, &missing) {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestPrepareDoesNotPushWhenCloneFails(t *testing.T) {
	r := &fakeRunner{fail: "clone"}
	c := &fakeCache{}
	m := &Mirror{Data: t.TempDir(), Token: "secret", Runner: r}
	_, err := m.Prepare(context.Background(), c, "/host", "owner/repo", "ses_1")
	if err == nil || c.called {
		t.Fatalf("error %v, cache called %v", err, c.called)
	}
}
