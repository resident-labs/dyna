package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

type profile struct {
	Provider       contract.ModelProvider `json:"provider"`
	Model          string                 `json:"model"`
	AllowedModels  []string               `json:"allowedModels"`
	Thinking       contract.ThinkingLevel `json:"thinking"`
	CompactTokens  int                    `json:"compactTokens,omitempty"`
	MaxConcurrent  int                    `json:"maxConcurrent,omitempty"`
	MaxCallsPerRun int                    `json:"maxCallsPerRun,omitempty"`
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
		if p.MaxConcurrent < 0 || p.MaxCallsPerRun < 0 {
			return nil, fmt.Errorf("profile %q: limits must be non-negative", name)
		}
		if p.CompactTokens < 0 {
			return nil, fmt.Errorf("profile %q: compactTokens must be non-negative", name)
		}
		if name == "" || p.Model == "" || !slices.Contains(p.AllowedModels, p.Model) {
			return nil, fmt.Errorf("profile %q: model must be allowed", name)
		}
		switch p.Provider {
		case contract.ModelProviderAnthropic, contract.ModelProviderOpenAI:
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

func (p ProfileConfig) List() []contract.ProfileSummary {
	result := make([]contract.ProfileSummary, 0, len(p))
	for name, profile := range p {
		result = append(result, contract.ProfileSummary{Name: name, Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking})
	}
	slices.SortFunc(result, func(a, b contract.ProfileSummary) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return result
}

func (s *Service) ProfileList(ctx context.Context, who contract.Principal, _ contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error) {
	if who.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, who.Session)
		if err != nil {
			return contract.ProfileListResponse{}, forbidden()
		}
		tree, err := s.store.RoomTree(ctx, session.Room)
		if err != nil {
			return contract.ProfileListResponse{}, failure(err)
		}
		if tree.Depth(who.Session) == 0 || s.checkDepth(tree.Depth(who.Session)+1) != nil {
			return contract.ProfileListResponse{}, forbidden()
		}
	} else if who.Kind != contract.PrincipalOwner {
		return contract.ProfileListResponse{}, forbidden()
	}
	return contract.ProfileListResponse{Profiles: s.profiles.List()}, nil
}

func (p ProfileConfig) CompactionTokens(name contract.ProfileName) int {
	return p[name].CompactTokens
}

func (p ProfileConfig) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	profile, ok := p[name]
	if !ok {
		return contract.ModelChoice{}, nil, fmt.Errorf("unknown profile %q", name)
	}
	return contract.ModelChoice{Provider: profile.Provider, ID: profile.Model, Thinking: profile.Thinking}, profile.AllowedModels, nil
}
