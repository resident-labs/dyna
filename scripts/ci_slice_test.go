package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		for _, name := range sliceTests(t, i, count, tests) {
			seen[name]++
		}
	}
	for _, name := range tests {
		if seen[name] != 1 {
			t.Fatalf("%s runs in %d slices, want 1", name, seen[name])
		}
	}
}

// sliceTests runs scripts/ci-slice.sh against a fake go command that lists tests.
func sliceTests(t *testing.T, index, count int, tests []string) []string {
	t.Helper()
	dir := t.TempDir()
	lines := []string{
		"#!/bin/sh",
		`if [ "$1" = test ] && [ "$2" = -list ]; then`,
		"cat <<'EOF'",
	}
	lines = append(lines, tests...)
	lines = append(lines, "EOF", "exit 0", "fi", "exit 1")
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(strings.Join(lines, "\n")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "ci-slice.sh", "dyna-agent/internal/pr/watch", fmt.Sprint(index), fmt.Sprint(count))
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ci-slice.sh %d %d: %v", index, count, err)
	}
	return strings.Fields(string(out))
}
