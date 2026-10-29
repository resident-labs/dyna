package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestSliceRunsEveryTestOnce runs the real slice script for every slice of one
// package. Each top-level test must run in exactly one slice, or a sliced
// package would skip tests on CI.
func TestSliceRunsEveryTestOnce(t *testing.T) {
	tests := []string{
		"TestA", "TestB", "TestC", "TestD", "TestE",
		"TestF", "TestG", "TestH", "TestI", "TestJ",
	}
	const count = 3
	seen := map[string]int{}
	for i := 0; i < count; i++ {
		out, err := runSlice(t, listingTests(tests), i, count)
		if err != nil {
			t.Fatalf("ci-slice.sh %d %d: %v", i, count, err)
		}
		for _, name := range strings.Fields(out) {
			seen[name]++
		}
	}
	for _, name := range tests {
		if seen[name] != 1 {
			t.Fatalf("%s runs in %d slices, want 1", name, seen[name])
		}
	}
}

// TestSliceFailsWhenListingFails checks that a package that cannot list its
// tests stops the slice. An empty slice would hide the package from CI.
func TestSliceFailsWhenListingFails(t *testing.T) {
	failing := []string{`echo "build failed" >&2`, "exit 1"}
	out, err := runSlice(t, failing, 0, 3)
	if err == nil {
		t.Fatalf("ci-slice.sh succeeded with no test list, printed %q", out)
	}
}

// listingTests is the fake go body that lists tests for go test -list.
func listingTests(tests []string) []string {
	body := []string{
		`if [ "$1" = test ] && [ "$2" = -list ]; then`,
		"cat <<'EOF'",
	}
	body = append(body, tests...)
	return append(body, "EOF", "exit 0", "fi", "exit 1")
}

// runSlice runs scripts/ci-slice.sh for one package against a fake go command.
func runSlice(t *testing.T, goBody []string, index, count int) (string, error) {
	t.Helper()
	dir := fakeGoDir(t, goBody)
	return runScript(t, dir, "ci-slice.sh", "dyna-agent/internal/pr/watch", fmt.Sprint(index), fmt.Sprint(count))
}
