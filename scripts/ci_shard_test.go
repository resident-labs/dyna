package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShardMapCoversEveryPackageOnce pins the properties a shard map must
// keep: every package is tested, heavy packages keep their measured assignments,
// and no other package is repeated. Only packages with explicit CI_SLICE
// assignments may repeat, and they run in "sliced" mode, never whole.
func TestShardMapCoversEveryPackageOnce(t *testing.T) {
	packages := []string{
		"dyna-agent/cmd/dyna",
		"dyna-agent/internal/engine",
		"dyna-agent/internal/harness",
		"dyna-agent/internal/harness/runtime",
		"dyna-agent/internal/hooks",
		"dyna-agent/internal/notify",
		"dyna-agent/internal/pr/view",
		"dyna-agent/internal/pr/watch",
		"dyna-agent/internal/roomsvc",
		"dyna-agent/internal/subagent",
		"dyna-agent/internal/visual",
		"dyna-agent/internal/workflow",
		"dyna-agent/internal/zzz-small",
	}
	sliced := map[string]string{
		"dyna-agent/internal/pr/watch": "0,7,8",
		"dyna-agent/internal/subagent": "1,9",
	}
	whole := map[string][]string{}
	slices := map[string][]string{}
	for i := 0; i < 10; i++ {
		for _, pkg := range shardPackages(t, i, 10, "whole", packages) {
			whole[pkg] = append(whole[pkg], fmt.Sprint(i))
		}
		for _, pkg := range shardPackages(t, i, 10, "sliced", packages) {
			slices[pkg] = append(slices[pkg], fmt.Sprint(i))
		}
	}
	for _, pkg := range packages {
		if want, ok := sliced[pkg]; ok {
			if len(whole[pkg]) != 0 {
				t.Fatalf("sliced package %s also runs whole on shards %v", pkg, whole[pkg])
			}
			if strings.Join(slices[pkg], ",") != want {
				t.Fatalf("sliced package %s is on shards %v, want %s", pkg, slices[pkg], want)
			}
			continue
		}
		if len(slices[pkg]) != 0 {
			t.Fatalf("whole package %s is in sliced mode on shards %v", pkg, slices[pkg])
		}
		got := whole[pkg]
		if len(got) == 0 {
			t.Fatalf("%s is on no shard", pkg)
		}
		if pkg == "dyna-agent/internal/visual" && strings.Join(got, ",") != "9" {
			t.Fatalf("internal/visual is on shards %v, want 9", got)
		}
		if pkg == "dyna-agent/internal/workflow" && strings.Join(got, ",") != "5" {
			t.Fatalf("internal/workflow is on shards %v, want 5", got)
		}
		if len(got) != 1 {
			t.Fatalf("%s is on shards %v, want exactly one", pkg, got)
		}
	}
}

// shardPackages runs the real script against a fake go command, so the test
// proves the script's own map without depending on this checkout's packages.
func shardPackages(t *testing.T, index, count int, mode string, packages []string) []string {
	t.Helper()
	dir := t.TempDir()
	lines := []string{
		"#!/bin/sh",
		`if [ "$1" = list ] && [ "$2" = -m ]; then echo dyna-agent; exit 0; fi`,
		`if [ "$1" = list ]; then`,
		"cat <<'EOF'",
	}
	lines = append(lines, packages...)
	lines = append(lines, "EOF", "exit 0", "fi", "exit 1")
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(strings.Join(lines, "\n")+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "ci-shard.sh", fmt.Sprint(index), fmt.Sprint(count), mode)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ci-shard.sh %d %d: %v", index, count, err)
	}
	return strings.Fields(string(out))
}
