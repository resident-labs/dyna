package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

// A session keeps its generation across turns. When an older pin names a Dyna
// command that no longer runs, the next turn must rebuild the generation, or
// its Codex or Claude thread starts an MCP server that cannot handshake and the
// turn has no Dyna tools.
func TestPrepareRebuildsPinWhoseDynaCommandIsGone(t *testing.T) {
	r, p := runtimeFixture(t, "codex")
	gone := filepath.Join(t.TempDir(), "gone")
	dead := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(dead, []byte("#!/bin/sh\nexec '"+gone+"' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Env = map[string]string{"DYNA_BIN": dead}
	if _, pin, err := r.Prepare(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	} else if pin.Generation == "" {
		t.Fatal("prepare pinned no generation")
	}
	_, first, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if harnesshome.Launches(dead) {
		t.Fatal("dead bridge reported as runnable")
	}

	live := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(live, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p.Env = map[string]string{"DYNA_BIN": live}
	_, rebuilt, err := r.Prepare(context.Background(), p, &first)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Generation == first.Generation {
		t.Fatal("turn reused a generation whose Dyna command is gone")
	}
	gen, err := loadNativeGeneration("codex", rebuilt.Generation, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if !harnesshome.Launches(gen.CLI) || !strings.Contains(strings.Join(gen.Args, " "), gen.CLI) {
		t.Fatalf("rebuilt generation still names an unusable Dyna command: %q", gen.CLI)
	}
}
