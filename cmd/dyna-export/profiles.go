package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/profile"
	"github.com/resident-labs/dyna/contract"
)

// These names are listed in the vendored proxy's model catalog. Unknown
// models are reported rather than inferred from a provider prefix.
var proxyModels = map[string]string{
	"haiku":                               "claude-haiku-5-5",
	"opus":                                "claude-opus-5-5",
	"sonnet":                              "claude-sonnet-5-5",
	"openai-codex/gpt-6-luna":             "gpt-6-luna",
	"openai-codex/gpt-6.1-terra":          "gpt-6.1-terra",
	"zai/glm-5.3":                         "glm-5.3",
	"zai/glm-5.3-flash":                   "glm-5.3-flash",
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
	Reason  contract.ProfileHandoffReason `json:"reason,omitempty"`
}

func readProfiles(config string) ([]contract.ProfileBundle, []string, []profileGap, []profileGap, error) {
	body, err := os.ReadFile(filepath.Join(config, "profiles.json"))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("captured profiles: %w", err)
	}
	var store profile.Store
	if err := json.Unmarshal(body, &store); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("decode profiles: %w", err)
	}
	if store.Version != 3 {
		return nil, nil, nil, nil, fmt.Errorf("unsupported profiles version %d", store.Version)
	}
	var result []contract.ProfileBundle
	var disabled []string
	var gaps []profileGap
	var dropped []profileGap
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
			if m == "meta/muse-wonder-full-1.0" {
				dropped = append(dropped, profileGap{Profile: p.Name, Model: m, Reason: contract.ProfileUnsupportedMuse})
				unknown = true
				continue
			}
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
		var thinking contract.ThinkingLevel
		for i := 0; i < len(p.ExtraArgs); i++ {
			arg := p.ExtraArgs[i]
			key, val, eq := strings.Cut(arg, "=")
			if key != "--effort" && key != "--thinking" {
				continue
			}
			if !eq {
				i++
				if i >= len(p.ExtraArgs) {
					return nil, nil, nil, nil, fmt.Errorf("profile thinking level is missing")
				}
				val = p.ExtraArgs[i]
			}
			level := contract.ThinkingLevel(val)
			switch level {
			case contract.ThinkingOff, contract.ThinkingMinimal, contract.ThinkingLow, contract.ThinkingMedium, contract.ThinkingHigh, contract.ThinkingXHigh:
			default:
				return nil, nil, nil, nil, fmt.Errorf("profile thinking level is unsupported")
			}
			if thinking != "" && thinking != level {
				return nil, nil, nil, nil, fmt.Errorf("conflicting profile thinking levels")
			}
			thinking = level
		}
		out := contract.ProfileBundle{Thinking: thinking, Name: contract.ProfileName(p.Name), Description: p.Description, Model: mapped[0],
			Fallback: contract.ProfileName(p.Fallback), Taste: p.Taste, Intelligence: p.Intelligence,
			Cost: p.Cost, Diagnostics: p.Diagnostics, MaxConcurrent: p.MaxConcurrent,
			Default: p.Default}
		if len(mapped) > 1 {
			out.Models = mapped
		}
		if p.Compact != nil {
			out.CompactTokens = int(p.Compact.Tokens)
		}
		result = append(result, out)
	}
	return result, disabled, gaps, dropped, nil
}
