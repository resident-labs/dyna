package scratch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateRoot points scratch at a private test base instead of the owner's
// real cache.
func isolateRoot(t *testing.T) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RootEnv, base)
}

func TestEnsurePrivateScratch(t *testing.T) {
	isolateRoot(t)
	for _, worker := range []string{"", "agent-1", "agent-2"} {
		path, err := Ensure(filepath.Join("owner", worker))
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !filepath.IsAbs(path) {
			t.Fatalf("scratch %s: %v, %v", path, info, err)
		}
		if path != filepath.Join(Root(), "owner", worker) {
			t.Fatal(path)
		}
	}
	for _, owner := range []string{"", ".", "..", "../escape", "/absolute"} {
		if _, err := Ensure(owner); err == nil {
			t.Fatalf("accepted owner %q", owner)
		}
	}
}

// A closing session's scratch folder goes at once, while the periodic pass keeps
// a closed session's folder for its retention.
func TestSweepClosingSessionRemovesItsScratch(t *testing.T) {
	for _, closing := range []bool{false, true} {
		t.Run(fmt.Sprintf("closing=%t", closing), func(t *testing.T) {
			isolateRoot(t)
			root := Root()
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1_800_000_000, 0)
			owner := filepath.Join(root, "ses_20261025-120000-abcd1234")
			if err := os.Mkdir(owner, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(owner, "note"), []byte("scratch"), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := Sweep(context.Background(), Options{
				Now:     now,
				Only:    filepath.Base(owner),
				Closing: closing,
				Logf:    t.Logf,
				Lookup:  func(string) Owner { return Owner{Known: true, ClosedAt: now.Add(-time.Minute)} },
			})
			if err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(owner)
			switch {
			case closing && !errors.Is(statErr, fs.ErrNotExist):
				t.Fatalf("a closing session kept its scratch: %v, %+v", statErr, result)
			case !closing && statErr != nil:
				t.Fatalf("the periodic pass removed a closed session's scratch inside its retention: %v, %+v", statErr, result)
			}
		})
	}
}

func TestSweepRetentionAndSymlinks(t *testing.T) {
	isolateRoot(t)
	root := Root()
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-4 * 24 * time.Hour)
	owners := map[string]time.Time{"closed-old": now.Add(-25 * time.Hour), "closed-recent": now.Add(-12 * time.Hour), "active": {}, "closed-link": now.Add(-25 * time.Hour)}
	for _, name := range []string{"closed-old", "closed-recent", "active", "unknown-old", "unknown-recent", "unknown-child-recent"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "note")
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
		date := old
		if name == "unknown-recent" {
			date = now
		}
		if name != "unknown-child-recent" {
			if err := os.Chtimes(file, date, date); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, date, date); err != nil {
			t.Fatal(err)
		}
	}
	// A link inside an expired directory must never delete its outside target.
	if err := os.Symlink(outside, filepath.Join(root, "closed-old", "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "unknown-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "closed-link")); err != nil {
		t.Fatal(err)
	}
	result, err := Sweep(context.Background(), Options{Now: now, Lookup: func(id string) Owner {
		closed, ok := owners[id]
		return Owner{Known: ok, Open: ok && closed.IsZero(), ClosedAt: closed}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 3 {
		t.Fatalf("removed: %#v", result)
	}
	for _, name := range []string{"closed-old", "unknown-old", "closed-link"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("kept %s: %v", name, err)
		}
	}
	for _, name := range []string{"closed-recent", "active", "unknown-recent", "unknown-child-recent", "unknown-link"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("removed %s: %v", name, err)
		}
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("outside changed: %q %v", data, err)
	}
}

func TestRejectSymlinkScratchRootAndOwner(t *testing.T) {
	isolateRoot(t)
	outside := t.TempDir()
	root := Root()
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure("owner"); err == nil {
		t.Fatal("accepted linked root")
	}
	if _, err := Sweep(context.Background(), Options{}); err == nil {
		t.Fatal("accepted linked cleanup root")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure("owner/agent-1"); err == nil {
		t.Fatal("accepted linked owner")
	}
}

func TestRejectSharedScratchRoot(t *testing.T) {
	isolateRoot(t)
	root := Root()
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure("owner"); err == nil {
		t.Fatal("accepted shared scratch root")
	}
	if _, err := Sweep(context.Background(), Options{}); err == nil {
		t.Fatal("accepted shared cleanup root")
	}
}

func TestSweepPressureOnlyAndBytes(t *testing.T) {
	isolateRoot(t)
	now := time.Now()
	for _, owner := range []string{"closed", "open", "unknown-old", "unknown-recent", "another"} {
		path, err := Ensure(owner)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "note"), []byte("12345"), 0600); err != nil {
			t.Fatal(err)
		}
		date := now.Add(-48 * time.Hour)
		if owner == "unknown-recent" {
			date = now
		}
		if err := os.Chtimes(filepath.Join(path, "note"), date, date); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, date, date); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(owner string) Owner {
		switch owner {
		case "closed":
			return Owner{Known: true, ClosedAt: now}
		case "open":
			return Owner{Known: true, Open: true}
		}
		return Owner{}
	}
	first, err := Sweep(context.Background(), Options{Now: now, Lookup: lookup, Only: "closed"})
	if err != nil || len(first.Removed) != 0 || len(first.Kept) != 1 {
		t.Fatalf("grace: %+v %v", first, err)
	}
	result, err := Sweep(context.Background(), Options{Now: now, Lookup: lookup, Only: "closed", Pressure: true})
	if err != nil || len(result.Removed) != 1 || result.BytesFreed != 5 {
		t.Fatalf("pressure only: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(Root(), "another")); err != nil {
		t.Fatal(err)
	}
	result, err = Sweep(context.Background(), Options{Now: now, Lookup: lookup, Pressure: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"open", "unknown-recent"} {
		if _, err := os.Stat(filepath.Join(Root(), owner)); err != nil {
			t.Fatalf("removed %s", owner)
		}
	}
	if _, err := os.Stat(filepath.Join(Root(), "unknown-old")); !os.IsNotExist(err) {
		t.Fatal("kept old unknown under pressure")
	}
}

func TestSweepCancellationAndInvalidOnly(t *testing.T) {
	isolateRoot(t)
	path, err := Ensure("owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Sweep(ctx, Options{}); err == nil {
		t.Fatal("ignored cancellation")
	}
	if _, err := Sweep(context.Background(), Options{Only: "../escape"}); err == nil {
		t.Fatal("accepted escaping owner")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

// Scratch lives on one disk base under the owner's cache. Each store gets a
// short namespace from its data folder, so a sweep in one store cannot reach
// another store's open sessions. TMPDIR, the harness HOME and the cache folder
// never move it.
func TestScratchRootUsesTheOwnerDiskBaseAndStoreNamespace(t *testing.T) {
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(owner.HomeDir, ".cache", "dyna", "scratch")
	tmp := t.TempDir()
	t.Setenv(RootEnv, "")
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := Root()
	if filepath.Dir(root) != base {
		t.Fatalf("scratch root = %q, want a namespace under %q", root, base)
	}
	if strings.HasPrefix(root, tmp) {
		t.Fatalf("scratch root is on TMPDIR: %q", root)
	}
	first := root
	for _, home := range []string{t.TempDir(), owner.HomeDir} {
		t.Setenv("HOME", home)
		if got := Root(); got != first {
			t.Fatalf("HOME %q moved the scratch root to %q", home, got)
		}
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	other := Root()
	if other == first || filepath.Dir(other) != base {
		t.Fatalf("another data folder did not get its own namespace under %q: %q", base, other)
	}
}

// A sweep in one store never reaches another store's scratch, even for a
// session the current store's lifecycle records do not know.
func TestSweepIsIsolatedByStore(t *testing.T) {
	isolateRoot(t)
	now := time.Now()
	old := now.Add(-4 * 24 * time.Hour)
	makeOld := func(owner string) string {
		t.Helper()
		dir, err := Ensure(owner)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "note")
		if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{file, dir} {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	first := makeOld("open-session")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	second := makeOld("old-session")
	result, err := Sweep(context.Background(), Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first, "note")); err != nil {
		t.Fatalf("a sweep removed another store's scratch: %v", err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("the current store's expired scratch remains: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != second {
		t.Fatalf("sweep removed %+v", result.Removed)
	}
}
