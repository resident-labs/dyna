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

// competingProfiles writes other Pi profiles that must not reach the active
// profile's generation: a smaller target and an unset target on a shared
// model.
func competingProfiles(t *testing.T) {
	t.Helper()
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, map[string]any{"profiles": []map[string]any{{
		"name": "small", "harness": "pi", "model": "shared/model",
		"compact": map[string]any{"tokens": 200000},
	}, {
		"name": "bare", "harness": "pi", "model": "shared/model",
	}}}, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The active profile's target reaches its whole model chain, fallbacks
// included, and no other profile's smaller or unset target on a shared model
// changes it. The chain model is absent from every model list.
func TestPiGenerationScopesTriggerToActiveProfile(t *testing.T) {
	competingProfiles(t)
	active := profile.Profile{Name: "active", Harness: profile.HarnessPi, Model: "shared/model", Models: []string{"shared/model", "openai-codex/gpt-6.1-terra"}, Compact: &profile.Compact{Tokens: 300000}}
	r, home, id := generationFixture(t, "pi")
	g := buildGeneration(t, r, "pi", home, id, active)
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
	want := map[string]int64{"shared/model": 300000, "openai-codex/gpt-6.1-terra": 300000}
	if len(got.Compaction.ModelOverrides) != len(want) {
		t.Fatalf("modelOverrides = %v, want exactly %v", got.Compaction.ModelOverrides, want)
	}
	for model, target := range want {
		entry, ok := got.Compaction.ModelOverrides[model]
		if !ok || entry.TriggerTokens != target {
			t.Fatalf("modelOverrides[%s] = %+v, want triggerTokens %d", model, entry, target)
		}
	}
}

// A profile without a target gets no trigger, even when another profile set
// a smaller target on the same model.
func TestPiGenerationNoTriggerWithoutTarget(t *testing.T) {
	competingProfiles(t)
	active := profile.Profile{Name: "bare", Harness: profile.HarnessPi, Model: "shared/model"}
	r, home, id := generationFixture(t, "pi")
	g := buildGeneration(t, r, "pi", home, id, active)
	raw, err := os.ReadFile(filepath.Join(g.Directory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Compaction struct {
			ModelOverrides map[string]json.RawMessage `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Compaction.ModelOverrides) != 0 {
		t.Fatalf("modelOverrides = %v, want none for a profile without a target", got.Compaction.ModelOverrides)
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
