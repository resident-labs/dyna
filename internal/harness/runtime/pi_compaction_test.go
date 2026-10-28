package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	accountadapters "dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/harness/config"
	"dyna-agent/internal/profile"
)

func TestPiDiscoveryHonorsCancellation(t *testing.T) {
	accounts := testAccounts(t)
	n, err := accounts.Create("pi", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	home, err := accounts.HarnessHome("pi")
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = (&Runtime{Accounts: accounts}).piGenerationReserves(ctx, home, n.ID, Bundle{Version: "1.4.0", Executable: exe})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("discovery error = %v, want cancellation", err)
	}
}

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

func TestPiProfileCompactionTargets(t *testing.T) {
	profiles := []profile.Profile{
		{Harness: profile.HarnessPi, Model: "p/shared", Models: []string{"p/shared", "p/fallback"}, Compact: &profile.Compact{Tokens: 200000}},
		{Harness: profile.HarnessPi, Model: "p/shared", Compact: &profile.Compact{Tokens: 100000}},
		{Harness: profile.HarnessPi, Model: "p/untouched"},
		{Harness: profile.HarnessClaudeCode, Model: "p/fallback", Compact: &profile.Compact{Tokens: 50000}},
	}
	got := piProfileTargets(profiles)
	if len(got) != 2 || got["p/shared"] != 100000 || got["p/fallback"] != 200000 {
		t.Fatalf("targets = %v", got)
	}
}

func TestPiCompactionReserves(t *testing.T) {
	models := []accountadapters.Model{
		{ID: "big/model", Context: 1000000},
		{ID: "medium/model", Context: 500000},
		{ID: "small/model", Context: 200000},
		{ID: "unknown/model"},
	}
	got := piCompactionReserves(models, map[string]int64{"big/model": 200000, "medium/model": 200000, "small/model": 200000})
	if got["big/model"] != 850000 || got["medium/model"] != 300050 || len(got) != 2 {
		t.Fatalf("reserves = %v", got)
	}
	if got := piCompactionReserves(models, nil); len(got) != 0 {
		t.Fatalf("disabled reserves = %v", got)
	}
	if got := piCompactionReserves(models, map[string]int64{"medium/model": 1}); got["medium/model"] > 499950 {
		t.Fatalf("reserve exceeds safe window: %v", got)
	}
}
