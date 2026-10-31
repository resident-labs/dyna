package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/profile"
	"github.com/resident-labs/dyna/contract"
)

// These names are listed in the vendored proxy's model catalog. Unknown
// models are reported rather than inferred from a provider prefix.
var proxyModels = map[string]string{
	"anthropic/claude-opus-5-5":           "claude-opus-5-5",
	"claude-opus-5-5":                     "claude-opus-5-5",
	"anthropic/claude-sonnet-5-5":         "claude-sonnet-5-5",
	"claude-sonnet-5-5":                   "claude-sonnet-5-5",
	"anthropic/claude-haiku-4-5-20251001": "claude-haiku-4-5-20251001",
	"claude-haiku-4-5-20251001":           "claude-haiku-4-5-20251001",
}

type profileGap struct {
	Profile string
	Model   string
}

func readProfiles(config string) ([]contract.ProfileBundle, []string, []profileGap, error) {
	body, err := os.ReadFile(filepath.Join(config, "profiles.json"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("captured profiles: %w", err)
	}
	var store profile.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return nil, nil, nil, fmt.Errorf("decode profiles: %w", err)
	}
	if store.Version != 3 {
		return nil, nil, nil, fmt.Errorf("unsupported profiles version %d", store.Version)
	}
	var result []contract.ProfileBundle
	var disabled []string
	var gaps []profileGap
	for _, p := range store.Profiles {
		if p.Disabled {
			disabled = append(disabled, p.Name)
			continue
		}
		models := p.Models
		if len(models) == 0 {
			models = []string{p.Model}
		}
		var mapped []string
		unknown := false
		for _, m := range models {
			model, ok := proxyModels[m]
			if !ok {
				gaps = append(gaps, profileGap{Profile: p.Name, Model: m})
				unknown = true
				continue
			}
			mapped = append(mapped, model)
		}
		if unknown {
			continue
		}
		out := contract.ProfileBundle{Name: contract.ProfileName(p.Name), Description: p.Description, Model: mapped[0],
			Fallback: contract.ProfileName(p.Fallback), Taste: p.Taste, Intelligence: p.Intelligence,
			Cost: p.Cost, Diagnostics: p.Diagnostics, MaxConcurrent: p.MaxConcurrent,
			MaxCallsPerRun: p.MaxCallsPerRun, Default: p.Default}
		if len(mapped) > 1 {
			out.Models = mapped
		}
		if p.Compact != nil {
			out.CompactTokens = int(p.Compact.Tokens)
		}
		result = append(result, out)
	}
	return result, disabled, gaps, nil
}
