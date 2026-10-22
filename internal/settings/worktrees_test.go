package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorktreesRootRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.WorktreesRoot != "~/.dyna-worktrees" || v.Source(KeyWorktreesRoot) != SourceDefault {
		t.Fatalf("default root = %q from %s", v.WorktreesRoot, v.Source(KeyWorktreesRoot))
	}
	root := filepath.Join(t.TempDir(), "managed trees")
	if err := v.Set(KeyWorktreesRoot, root); err != nil {
		t.Fatal(err)
	}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreesRoot != root || got.Source(KeyWorktreesRoot) != SourceFile {
		t.Fatalf("stored root = %q from %s", got.WorktreesRoot, got.Source(KeyWorktreesRoot))
	}
	if err := got.Set(KeyWorktreesRoot, " \t"); err == nil || got.WorktreesRoot != root {
		t.Fatalf("empty root changed settings: %q, %v", got.WorktreesRoot, err)
	}
}

func TestLoadRejectsInvalidWorktreesRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	for _, raw := range []string{`{"worktrees":{"root":""}}`, `{"worktrees":{"root":42}}`} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid root: %s", raw)
		}
	}
}

func TestWorktreesMinFreeBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	v, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v.WorktreesMinFreeBytes != 10<<30 || v.Source(KeyWorktreesMinFreeBytes) != SourceDefault {
		t.Fatalf("default minimum = %d", v.WorktreesMinFreeBytes)
	}
	for _, tc := range []struct {
		value string
		want  int64
	}{{"2 GiB", 2 << 30}, {"0", 0}} {
		if err := v.Set(KeyWorktreesMinFreeBytes, tc.value); err != nil {
			t.Fatal(err)
		}
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if got.WorktreesMinFreeBytes != tc.want || got.Source(KeyWorktreesMinFreeBytes) != SourceFile {
			t.Fatalf("minimum = %d from %s", got.WorktreesMinFreeBytes, got.Source(KeyWorktreesMinFreeBytes))
		}
	}
	for _, value := range []string{"-1", "large", "1.5 bytes"} {
		if err := v.Set(KeyWorktreesMinFreeBytes, value); err == nil {
			t.Fatalf("accepted minimum %q", value)
		}
	}
}
