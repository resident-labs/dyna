package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	accountadapters "dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/harness/config"
)

func TestPiCompactionGenerationPreservesOverrides(t *testing.T) {
	dir := t.TempDir()
	prefs := map[string]any{"compaction": map[string]any{
		"enabled":        true,
		"modelOverrides": map[string]any{"p/existing": map[string]any{"reserveTokens": 123, "keepRecentTokens": 456}},
	}}
	_, err := materializeNative("pi", dir, dir, dir, "", config.HarnessConfig{}, prefs, map[string]int64{"p/existing": 300000, "p/new": 400000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Compaction struct {
			Enabled bool `json:"enabled"`
			Models  map[string]struct {
				Reserve int64 `json:"reserveTokens"`
				Recent  int64 `json:"keepRecentTokens"`
			} `json:"modelOverrides"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Compaction.Enabled || got.Compaction.Models["p/existing"].Reserve != 123 || got.Compaction.Models["p/existing"].Recent != 456 || got.Compaction.Models["p/new"].Reserve != 400000 {
		t.Fatalf("compaction = %+v", got.Compaction)
	}
}

func TestPiCompactionReserves(t *testing.T) {
	models := []accountadapters.Model{
		{ID: "big/model", Context: 1000000},
		{ID: "medium/model", Context: 500000},
		{ID: "small/model", Context: 200000},
		{ID: "unknown/model"},
	}
	got := piCompactionReserves(models, 200000)
	if got["big/model"] != 850000 || got["medium/model"] != 300050 || len(got) != 2 {
		t.Fatalf("reserves = %v", got)
	}
	if got := piCompactionReserves(models, 0); len(got) != 0 {
		t.Fatalf("disabled reserves = %v", got)
	}
	if got := piCompactionReserves(models, 1); got["medium/model"] > 499950 {
		t.Fatalf("reserve exceeds safe window: %v", got)
	}
}
