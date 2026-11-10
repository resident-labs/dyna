package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestMirrorProvenanceBlocksOfflineIdentityChanges(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "engine-to-account", true: "account-to-engine"}[reverse], func(t *testing.T) {
			f := newSeedFixture(t)
			login := ""
			if reverse {
				login = "work"
			}
			f.mirror.ResolveToken = func(context.Context, contract.RepoName) (string, string, error) { return "token", login, nil }
			if reverse {
				if _, _, err := f.mirror.Seed(t.Context(), gitCache{t}, "owner/repo"); err != nil {
					t.Fatal(err)
				}
			}
			if reverse {
				login = ""
			} else {
				login = "work"
			}
			f.github.hang.Store(true)
			// Verification being unreachable cannot authorize old-identity objects.
			f.mirror.VerifyAccount = func(context.Context, contract.RepoName, string, string) error { return nil }
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if _, err := f.mirror.Prepare(ctx, gitCache{t}, "owner/repo", false); err == nil {
				t.Fatal("served previous identity's mirror offline")
			}
			// A failed refill leaves a durable requirement, including across restarts.
			f.mirror.fetched.Clear()
			for _, read := range []func(context.Context) error{
				func(ctx context.Context) error { return f.mirror.Refresh(ctx, "owner/repo") },
				func(ctx context.Context) error { _, err := f.mirror.Default(ctx, "owner/repo"); return err },
				func(ctx context.Context) error { _, err := f.mirror.Branches(ctx, "owner/repo"); return err },
				func(ctx context.Context) error { _, err := f.mirror.ComposeNames(ctx, "owner/repo"); return err },
				func(ctx context.Context) error {
					_, err := f.mirror.ComposeFile(ctx, "owner/repo", "default")
					return err
				},
			} {
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				if err := read(ctx); err == nil {
					t.Fatal("served discarded mirror through compose")
				}
				cancel()
			}
		})
	}
}

func TestUnmarkedEngineMirrorStillWorksOffline(t *testing.T) {
	f := newSeedFixture(t)
	path, err := f.mirror.Layout.Git("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture fetched and pushed once. A subsequent ordinary fetch/push
	// must still leave the engine mirror unmarked.
	if _, err := f.mirror.Prepare(t.Context(), gitCache{t}, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, box.GitIdentityMarker)); !os.IsNotExist(err) {
		t.Fatalf("ordinary fetch created provenance marker: %v", err)
	}
	f.github.hang.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	cache := &fakeCache{}
	item, err := f.mirror.Prepare(ctx, cache, "owner/repo", false)
	if cache.called {
		t.Fatal("unmarked offline engine prepare pushed the host cache")
	}
	if err != nil || item.Base != "main" {
		t.Fatalf("legacy engine offline setup: %+v %v", item, err)
	}
}
