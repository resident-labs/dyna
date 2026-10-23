package runtime

import (
	"dyna-agent/internal/profile"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyDormantSessionKeepsRuntimePins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	id := "ses_20261019-000001-aaaaaaaa"
	r := &Runtime{Generations: filepath.Join(store.DataDir(), "generations")}
	generation := filepath.Join(r.Generations, "gen-v1-fixture")
	pin := Pin{Bundle: Bundle{Harness: "codex", Version: "1.0.0"}, Generation: generation}
	root := filepath.Join(store.DataDir(), "sessions")
	recordtest.Write(t, root, id, map[string]any{"id": id, "finished": time.Now(), "status": "dormant", "runtimePin": pin})
	if err := os.MkdirAll(filepath.Dir(profile.DefaultPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(profile.DefaultPath(), map[string]any{"profiles": []any{}}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Check(root, id); err != nil {
		t.Fatal(err)
	}
	keep := map[string]bool{}
	if err := r.openGenerationPins(keep); err != nil || !keep[generation] {
		t.Errorf("legacy dormant generation pin lost: %+v, %v", keep, err)
	}
	pins, err := OpenPins()
	if err != nil || len(pins) != 1 || pins[0].Version != pin.Bundle.Version {
		t.Fatalf("legacy dormant bundle pin lost: %+v, %v", pins, err)
	}
}
