package folder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportScopedChannelNames(t *testing.T) {
	snapshot := Snapshot{Entries: []Entry{
		{Name: "Community", Category: &Category{Identity: Identity{ID: "10"}}},
		{Name: "Staff", Category: &Category{Identity: Identity{ID: "11"}}},
		{Name: "general", Parent: "Community", Channel: &Channel{Identity: Identity{ID: "20"}}},
		{Name: "general", Parent: "Staff", Channel: &Channel{Identity: Identity{ID: "21"}}},
	}}
	directory := t.TempDir()
	if _, err := Export(directory, snapshot, false); err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"Community", "Staff"} {
		if _, err := os.Stat(filepath.Join(directory, "channels", parent, "general", "channel.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	snapshot.Entries = append(snapshot.Entries, Entry{Name: "general", Parent: "Community", Channel: &Channel{Identity: Identity{ID: "22"}}})
	_, err := Export(t.TempDir(), snapshot, false)
	if err == nil || !strings.Contains(err.Error(), `"20"`) || !strings.Contains(err.Error(), `"22"`) {
		t.Fatalf("duplicate must identify both IDs: %v", err)
	}
}

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
