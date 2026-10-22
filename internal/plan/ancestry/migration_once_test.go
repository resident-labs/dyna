package ancestry_test

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store/storetest"
)

func TestEmptyAncestryCannotBeFilledByChangedHeader(t *testing.T) {
	dataDir := storetest.DataDir(t)
	t.Cleanup(func() { database.Close(dataDir) })
	dir := filepath.Join(dataDir, "runs", "wf_empty-import")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(path, []byte("{\"version\":1,\"resumedFrom\":\"\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	s := ancestry.Store{DataDir: dataDir}
	if parent, err := s.Parent(t.Context(), dir); err != nil || parent != "" {
		t.Fatalf("initial parent = %q, %v", parent, err)
	}
	if err := os.WriteFile(path, []byte("{\"version\":1,\"resumedFrom\":\"wf_stale\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := ancestry.Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	if parent, err := s.Parent(t.Context(), dir); err != nil || parent != "" {
		t.Fatalf("changed header filled canonical empty parent: %q, %v", parent, err)
	}
}
