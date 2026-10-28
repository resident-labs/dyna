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

// The profile model runs on derived metadata and is absent from every model
// list, so per-model reserves never cover it. The generation must still carry
// its compaction target through the global triggerTokens setting.
func TestPiGenerationWritesCompactionTrigger(t *testing.T) {
	r, home, id := generationFixture(t, "pi")
	path := profile.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, map[string]any{"profiles": []map[string]any{{
		"name": "terra", "harness": "pi", "model": "openai-codex/gpt-6.1-terra",
		"compact": map[string]any{"tokens": 200000},
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
			TriggerTokens int64 `json:"triggerTokens"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Compaction.TriggerTokens != 200000 {
		t.Fatalf("compaction.triggerTokens = %d, want 200000 for an unlisted model", got.Compaction.TriggerTokens)
	}
}

// The generation writes only triggerTokens, and never over the owner's own
// compaction preferences.
func TestPiCompactionTriggerPreservesOwnerSettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefs  map[string]any
		want   int64
		wantOK bool
	}{
		{"owner trigger stays", map[string]any{"compaction": map[string]any{"enabled": true, "triggerTokens": 150000}}, 150000, true},
		{"unset trigger written", map[string]any{"compaction": map[string]any{"enabled": true}}, 200000, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := materializeNative("pi", dir, dir, dir, "", config.HarnessConfig{}, tc.prefs, 200000, nil); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Compaction struct {
					Enabled       bool  `json:"enabled"`
					TriggerTokens int64 `json:"triggerTokens"`
				} `json:"compaction"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !got.Compaction.Enabled || got.Compaction.TriggerTokens != tc.want {
				t.Fatalf("compaction = %+v, want trigger %d with the owner settings kept", got.Compaction, tc.want)
			}
		})
	}
}
