//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCgroupsEnablesRootControllers(t *testing.T) {
	root := t.TempDir()
	guard, err := PrepareCgroups(root)
	if err != nil {
		t.Fatal(err)
	}
	if guard.root != filepath.Join(root, "jobs") {
		t.Fatalf("jobs root: %s", guard.root)
	}
	controllers, err := os.ReadFile(filepath.Join(root, "cgroup.subtree_control"))
	if err != nil {
		t.Fatal(err)
	}
	if string(controllers) != "+cpu +memory +pids" {
		t.Fatalf("controllers: %q", controllers)
	}
}
