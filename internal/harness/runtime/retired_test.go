package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/store"
)

// GC drops retired Cursor records and Dyna's leftover Cursor folders.
func TestGCDropsRetiredCursorAccountsAndFolders(t *testing.T) {
	a := testAccounts(t)
	bundles := &BundleManager{Root: filepath.Join(filepath.Dir(a.Root), "harnesses")}
	survivor, err := a.Create("pi", "work")
	if err != nil {
		t.Fatal(err)
	}
	cursorHome := filepath.Join(a.Root, "cursor-agent", "0123456789abcdef0123456789abcdef")
	bundle := filepath.Join(bundles.Root, "cursor-agent", "2026.10.16-e3cb207")
	for _, dir := range []string{cursorHome, bundle, filepath.Join(bundles.Root, "pi", "1.3.0")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Installed bundles are read-only.
	if err := os.Chmod(bundle, 0500); err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Root: a.Root}
	old := store.Entry{Metadata: store.Metadata{ID: "0123456789abcdef0123456789abcdef", Harness: "cursor-agent", NativeHome: cursorHome}}
	if err := s.Update(func(st *store.State) error { st.Entries = append(st.Entries, old); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRetiredHarnesses(a, bundles); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil || len(state.Entries) != 1 || state.Entries[0].ID != survivor.ID {
		t.Fatalf("store after gc = %+v, %v", state.Entries, err)
	}
	for _, dir := range []string{filepath.Join(a.Root, "cursor-agent"), filepath.Join(bundles.Root, "cursor-agent")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", dir, err)
		}
	}
	for _, dir := range []string{survivor.Home, filepath.Join(bundles.Root, "pi", "1.3.0")} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("removed %s: %v", dir, err)
		}
	}
}

// qwen-audio-agent's leftover bundle, from before the Go voice service, is
// deleted by gc; other bundles stay.
func TestGCRemovesTheRetiredQwenBundle(t *testing.T) {
	a := testAccounts(t)
	bundles := &BundleManager{Root: filepath.Join(filepath.Dir(a.Root), "harnesses")}
	qwen := filepath.Join(bundles.Root, "qwen-audio-agent", "2.0.1")
	survivor := filepath.Join(bundles.Root, "pi", "1.3.0")
	for _, dir := range []string{qwen, survivor} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(qwen, 0500); err != nil { // installed bundles are read-only
		t.Fatal(err)
	}
	if err := RemoveRetiredHarnesses(a, bundles); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Dir(qwen)); !os.IsNotExist(err) {
		t.Fatalf("the qwen bundle folder is still there: %v", err)
	}
	if _, err := os.Lstat(survivor); err != nil {
		t.Fatalf("gc removed another bundle: %v", err)
	}
}

// An Antigravity account saved by an earlier release must not break listing or
// lookup for the accounts that survive. gc then drops the record and folders.
func TestRetiredAntigravityAccountIsSkippedUntilGC(t *testing.T) {
	a := testAccounts(t)
	survivor, err := a.Create("pi", "work")
	if err != nil {
		t.Fatal(err)
	}
	const oldID = "0123456789abcdef0123456789abcdef"
	home := filepath.Join(a.Root, "antigravity", oldID)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	s := &store.Store{Root: a.Root}
	old := store.Entry{Metadata: store.Metadata{ID: oldID, Harness: "antigravity", Label: "google", NativeHome: home}}
	if err := s.Update(func(st *store.State) error { st.Entries = append(st.Entries, old); return nil }); err != nil {
		t.Fatal(err)
	}
	rows, err := a.List()
	if err != nil || len(rows) != 1 || rows[0].ID != survivor.ID {
		t.Fatalf("list = %+v, %v", rows, err)
	}
	if _, err := a.Find(oldID); !errors.Is(err, errAccountMissing) {
		t.Fatalf("found a retired account: %v", err)
	}
	if err := RemoveRetiredHarnesses(a, &BundleManager{Root: filepath.Join(filepath.Dir(a.Root), "harnesses")}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read()
	if err != nil || len(state.Entries) != 1 || state.Entries[0].ID != survivor.ID {
		t.Fatalf("store after gc = %+v, %v", state.Entries, err)
	}
	if _, err := os.Lstat(filepath.Join(a.Root, "antigravity")); !os.IsNotExist(err) {
		t.Fatalf("retired account folder is still there: %v", err)
	}
}
