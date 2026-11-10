package git

import (
	"context"
	"errors"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestAccountSeedDoesNotUseCacheAfterCredentialLoss(t *testing.T) {
	f := newSeedFixture(t)
	missing := false
	f.mirror.ResolveToken = func(context.Context, contract.RepoName) (string, string, error) {
		if missing {
			return "", "work", errors.New("GitHub account work of room itasca: credential unavailable")
		}
		return "work-token", "work", nil
	}
	// Fill both mirror and host cache with the selected account first.
	if _, _, err := f.mirror.Seed(t.Context(), gitCache{t}, "owner/repo"); err != nil {
		t.Fatal(err)
	}
	missing = true
	if _, _, err := f.mirror.Seed(t.Context(), gitCache{t}, "owner/repo"); err == nil {
		t.Fatal("seed used existing mirror after credential loss")
	}
	for _, fetch := range []bool{false, true} {
		if _, err := f.mirror.Prepare(t.Context(), gitCache{t}, "owner/repo", fetch); err == nil {
			t.Fatalf("prepare fetch=%v used existing mirror", fetch)
		}
	}
	if _, err := f.mirror.ComposeNames(t.Context(), "owner/repo"); err == nil {
		t.Fatal("compose names used existing mirror")
	}
	if _, err := f.mirror.ComposeFile(t.Context(), "owner/repo", "default"); err == nil {
		t.Fatal("compose file used existing mirror")
	}
}

func TestAccountCacheRefusesAuthorizationFailures(t *testing.T) {
	f := newSeedFixture(t)
	f.mirror.ResolveToken = func(context.Context, contract.RepoName) (string, string, error) { return "work-token", "work", nil }
	denied := errors.New("GitHub account work of room itasca: GitHub HTTP 403: Forbidden")
	f.mirror.VerifyAccount = func(context.Context, contract.RepoName, string, string) error { return denied }
	// Even a recent successful fetch cannot bypass the access check.
	f.mirror.fetched.Store(contract.RepoName("owner/repo"), f.mirror.Now())
	if _, _, err := f.mirror.Seed(t.Context(), gitCache{t}, "owner/repo"); !errors.Is(err, denied) {
		t.Fatalf("seed: %v", err)
	}
	if _, err := f.mirror.Prepare(t.Context(), gitCache{t}, "owner/repo", true); !errors.Is(err, denied) {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.mirror.ComposeNames(t.Context(), "owner/repo"); !errors.Is(err, denied) {
		t.Fatalf("compose: %v", err)
	}
}
