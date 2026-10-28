package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

// A session keeps its generation across turns. When an older pin names a Dyna
// command that no longer runs, the next turn must rebuild the generation, or
// its Pi or Claude thread starts an MCP server that cannot handshake and the
// turn has no Dyna tools.
func TestPrepareRebuildsPinWhoseDynaCommandIsGone(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	noInstalledDyna(t)
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

// A pin created before Dyna passed the compaction trigger per invocation can
// carry the compaction overrides older Dyna code wrote into its settings.
// Resuming must rebuild it, or the republished reserves reach every session on
// the account and cap the invocation trigger below its target.
func TestPrepareRebuildsPinWithLegacyCompactionOverrides(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	_, first, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(first.Generation), generationPrefix+strings.Repeat("a", 64))
	if err := os.Rename(first.Generation, stale); err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(map[string]any{"compaction": map[string]any{"modelOverrides": map[string]any{"zai/glm-5.3": map[string]any{"reserveTokens": 150000}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "settings.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	_, rebuilt, err := r.Prepare(context.Background(), p, &Pin{AccountID: first.AccountID, Bundle: first.Bundle, Generation: stale})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Generation == stale {
		t.Fatal("prepare reused a pin with legacy compaction overrides")
	}
	raw, err := os.ReadFile(filepath.Join(rebuilt.Generation, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Compaction *struct {
			ModelOverrides map[string]json.RawMessage `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Compaction != nil {
		for model, entry := range doc.Compaction.ModelOverrides {
			if strings.Contains(string(entry), "reserveTokens") {
				t.Fatalf("rebuilt generation kept a reserve override for %s", model)
			}
		}
	}
}
