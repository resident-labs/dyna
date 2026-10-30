package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"

	"github.com/resident-labs/dyna/contract"
)

type profile struct {
	Provider      contract.ModelProvider `json:"provider"`
	Model         string                 `json:"model"`
	AllowedModels []string               `json:"allowedModels"`
	Thinking      contract.ThinkingLevel `json:"thinking"`
}

type ProfileConfig map[contract.ProfileName]profile

// LoadProfiles validates configuration at the file boundary.
func LoadProfiles(path string) (ProfileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var profiles ProfileConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profiles); err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profiles: no profiles")
	}
	for name, p := range profiles {
		if name == "" || p.Model == "" || !slices.Contains(p.AllowedModels, p.Model) {
			return nil, fmt.Errorf("profile %q: model must be allowed", name)
		}
		switch p.Provider {
		case contract.ProviderAnthropic, contract.ProviderOpenAI:
		default:
			return nil, fmt.Errorf("profile %q: invalid provider", name)
		}
		switch p.Thinking {
		case contract.ThinkingOff, contract.ThinkingMinimal, contract.ThinkingLow, contract.ThinkingMedium, contract.ThinkingHigh, contract.ThinkingXHigh:
		default:
			return nil, fmt.Errorf("profile %q: invalid thinking level", name)
		}
	}
	return profiles, nil
}

func (p ProfileConfig) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	profile, ok := p[name]
	if !ok {
		return contract.ModelChoice{}, nil, fmt.Errorf("unknown profile %q", name)
	}
	return contract.ModelChoice{Provider: profile.Provider, ID: profile.Model, Thinking: profile.Thinking}, profile.AllowedModels, nil
}
