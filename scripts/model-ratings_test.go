package main

import (
	"os"
	"os/exec"
	"testing"
)

// The Artificial Analysis comparison table names a model with its vendor and
// reasoning level, which once hid the baseline column and left every rating at
// n/a. Column picking stays a pure function, so one Node test covers it with no
// browser and no network.
func TestModelRatingColumnsPickTheBaseline(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is unavailable")
	}
	// Node reads these files, and Go's test cache only follows files this
	// binary opens. Reading them here makes an edit to either one rerun the
	// test instead of returning a stale pass.
	for _, name := range []string{"model-ratings-columns.mjs", "model-ratings-columns.test.mjs"} {
		if _, err := os.ReadFile(name); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
	}
	out, err := exec.CommandContext(t.Context(), node, "--test", "model-ratings-columns.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("column picking: %v\n%s", err, out)
	}
}
