package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

// A session keeps its generation across turns. When an older pin names a Dyna
// command that no longer runs, the next turn must rebuild the generation, or
// its Pi or Claude thread starts an MCP server that cannot handshake and the
// turn has no Dyna tools.
func TestPrepareRebuildsPinWhoseDynaCommandIsGone(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
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
	gen, err := loadNativeGeneration("pi", rebuilt.Generation, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	// Pi takes the Dyna command from its environment, so the bridge is the
	// only place its pinned command shows.
	if !harnesshome.Launches(gen.CLI) {
		t.Fatalf("rebuilt generation still names an unusable Dyna command: %q", gen.CLI)
	}
}
