package workflowresolve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, ".dyna", "workflows")
	personal := filepath.Join(root, "personal")
	for _, dir := range []string{project, personal} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "deep-research.js"), []byte(dir), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{project, personal} {
		path, source, err := Resolve("deep-research", "", root, personal)
		if err != nil || path != filepath.Join(dir, "deep-research.js") || source != dir {
			t.Fatalf("got %q %q %v", path, source, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	path, _, err := Resolve("deep-research", "", root, personal)
	if err != nil || path != "bundled:deep-research.js" {
		t.Fatalf("bundled: %q %v", path, err)
	}
}
