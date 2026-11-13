package folder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportNonemptyAndOpaqueUnmanaged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(dir, Snapshot{}, false); err == nil {
		t.Fatal("nonempty unrelated root accepted")
	}
	subtree := filepath.Join(dir, "channels", "external")
	if err := os.MkdirAll(subtree, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subtree, "UNMANAGED.md"), []byte("External owner\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An unmanaged descendant must not even be inspected for symlinks.
	if err := os.Symlink("/not/a/real/path", filepath.Join(subtree, "opaque")); err != nil {
		t.Fatal(err)
	}
	report, err := Export(dir, Snapshot{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || report[0] != "channels/external: unmanaged: External owner" {
		t.Fatal(report)
	}
}
