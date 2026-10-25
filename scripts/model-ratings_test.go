package main

import (
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
	out, err := exec.CommandContext(t.Context(), node, "--test", "model-ratings-columns.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("column picking: %v\n%s", err, out)
	}
}
