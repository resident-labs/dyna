package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/store"
)

// The profile models run on derived metadata and are absent from every model
// list, so per-model discovery never covers them. Each profile's target must
// still reach its own model through a modelOverrides trigger.
func TestPiGenerationWritesCompactionTrigger(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, map[string]any{"profiles": []map[string]any{{
		"name": "terra", "harness": "pi", "model": "openai-codex/gpt-6.1-terra",
		"compact": map[string]any{"tokens": 200000},
	}, {
		"name": "fast", "harness": "pi", "model": "anthropic/claude-haiku",
		"compact": map[string]any{"tokens": 120000},
	}, {
		"name": "plain", "harness": "pi", "model": "google/gemini-pro",
	}}}, 0o600); err != nil {
		t.Fatal(err)
	}
	g := buildGeneration(t, r, "pi", home, id)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Compaction struct {
			ModelOverrides map[string]struct {
				TriggerTokens int64 `json:"triggerTokens"`
			} `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"openai-codex/gpt-6.1-terra": 200000, "anthropic/claude-haiku": 120000}
	if len(got.Compaction.ModelOverrides) != len(want) {
		t.Fatalf("modelOverrides = %v, want exactly %v (a profile without a target adds none)", got.Compaction.ModelOverrides, want)
	}
	for model, target := range want {
		entry, ok := got.Compaction.ModelOverrides[model]
		if !ok || entry.TriggerTokens != target {
			t.Fatalf("modelOverrides[%s] = %+v, want triggerTokens %d", model, entry, target)
		}
	}
}

// The generation writes only per-model triggers and never over the owner's
// own compaction preferences.
func TestPiCompactionTriggerPreservesOwnerSettings(t *testing.T) {
	dir := t.TempDir()
	prefs := map[string]any{"compaction": map[string]any{
		"enabled":        true,
		"modelOverrides": map[string]any{"openai-codex/gpt-6.1-terra": map[string]any{"triggerTokens": 150000}},
	}}
	triggers := map[string]int64{"openai-codex/gpt-6.1-terra": 200000, "anthropic/claude-haiku": 120000}
	if _, err := materializeNative("pi", dir, dir, dir, "", config.HarnessConfig{}, prefs, triggers, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Compaction struct {
			Enabled        bool `json:"enabled"`
			ModelOverrides map[string]struct {
				TriggerTokens int64 `json:"triggerTokens"`
			} `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Compaction.Enabled {
		t.Fatal("owner compaction.enabled was dropped")
	}
	want := map[string]int64{"openai-codex/gpt-6.1-terra": 150000, "anthropic/claude-haiku": 120000}
	for model, target := range want {
		entry, ok := got.Compaction.ModelOverrides[model]
		if !ok || entry.TriggerTokens != target {
			t.Fatalf("modelOverrides[%s] = %+v, want triggerTokens %d (owner value kept)", model, entry, target)
		}
	}
}
