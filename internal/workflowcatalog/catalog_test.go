package workflowcatalog

import (
	"dyna-agent/internal/workflowresolve"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveListShow(t *testing.T) {
	root := t.TempDir()
	personal := filepath.Join(root, "personal")
	source := "export const meta = {name: 'sample', description: 'A sample'}"
	if err := Save("sample", source, root, personal, true, false); err != nil {
		t.Fatal(err)
	}
	if err := Save("sample", source, root, personal, true, false); !os.IsExist(err) {
		t.Fatalf("expected overwrite refusal: %v", err)
	}
	if err := Save("../bad", source, root, personal, false, false); err == nil {
		t.Fatal("invalid name accepted")
	}
	items, err := List(root, personal)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Name == "sample" && item.Scope == "project" && item.Description == "A sample" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing project workflow: %+v", items)
	}
	_, got, err := workflowresolve.Resolve("sample", "", root, personal)
	if err != nil || got != source {
		t.Fatalf("show: %q %v", got, err)
	}
	if err := Save("sample", "new", root, personal, true, true); err != nil {
		t.Fatal(err)
	}
}
