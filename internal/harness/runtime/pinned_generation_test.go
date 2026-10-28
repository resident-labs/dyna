package runtime

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesshome "dyna-agent/internal/harness/home"
)

// installLegacyBundle clones the installed fork bundle under an older version
// name with a matching manifest: the layout that version's installer left on
// disk when it was still supported.
func installLegacyBundle(t *testing.T, m *BundleManager, h, version string) {
	t.Helper()
	from := filepath.Join(m.Root, h, piForkVersion)
	to := filepath.Join(m.Root, h, version)
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(to, "bundle.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest bundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Version = version
	if raw, err = json.Marshal(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(to, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && entry.IsDir() {
			return os.Chmod(path, 0700)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

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

// A session pinned before the fork level rose still resumes: resolving the
// pinned version fails as unusable, so Dyna falls back to the newest supported
// installed bundle and rebuilds the pin's generation for it.
func TestPrepareResumesALegacyForkPinOnASupportedBundle(t *testing.T) {
	r, p := runtimeFixture(t, "pi")
	// The level Dyna shipped before this change, still installed on disk.
	installLegacyBundle(t, r.Bundles, "pi", "1.4.0-dyna.2")
	_, first, err := r.Prepare(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := Pin{AccountID: first.AccountID, Bundle: Bundle{Harness: "pi", Version: "1.4.0-dyna.2"}, Generation: first.Generation}
	selected, resumed, err := r.Prepare(context.Background(), p, &legacy)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Bundle.Version != piForkVersion {
		t.Fatalf("resume kept the unsupported bundle %q", resumed.Bundle.Version)
	}
	if selected.RuntimeExecutable != resumed.Bundle.Executable {
		t.Fatal("resume launches a bundle other than the pinned one")
	}
	if resumed.Generation == "" {
		t.Fatal("resume rebuilt no generation")
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
