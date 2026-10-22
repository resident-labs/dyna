package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/account/store"
)

// GC drops retired Cursor records and Dyna's leftover Cursor folders.
func TestGCDropsRetiredCursorAccountsAndFolders(t *testing.T) {
	a := testAccounts(t)
	bundles := &BundleManager{Root: filepath.Join(filepath.Dir(a.Root), "harnesses")}
	codex, err := a.Create("codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	cursorHome := filepath.Join(a.Root, "cursor-agent", "0123456789abcdef0123456789abcdef")
	bundle := filepath.Join(bundles.Root, "cursor-agent", "2026.10.16-e3cb207")
	for _, dir := range []string{cursorHome, bundle, filepath.Join(bundles.Root, "codex", "1.0.0")} {
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
	if err != nil || len(state.Entries) != 1 || state.Entries[0].ID != codex.ID {
		t.Fatalf("store after gc = %+v, %v", state.Entries, err)
	}
	for _, dir := range []string{filepath.Join(a.Root, "cursor-agent"), filepath.Join(bundles.Root, "cursor-agent")} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", dir, err)
		}
	}
	for _, dir := range []string{codex.Home, filepath.Join(bundles.Root, "codex", "1.0.0")} {
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
	codex := filepath.Join(bundles.Root, "codex", "1.0.0")
	for _, dir := range []string{qwen, codex} {
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
	if _, err := os.Lstat(codex); err != nil {
		t.Fatalf("gc removed another bundle: %v", err)
	}
}
