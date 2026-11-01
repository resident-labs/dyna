package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type profile struct {
	Description   string                 `json:"description"`
	Taste         int                    `json:"taste"`
	Diagnostics   int                    `json:"diagnostics"`
	Intelligence  int                    `json:"intelligence"`
	Cost          int                    `json:"cost"`
	Provider      contract.ModelProvider `json:"provider"`
	Model         string                 `json:"model"`
	AllowedModels []string               `json:"allowedModels"`
	Thinking      contract.ThinkingLevel `json:"thinking"`
	CompactTokens int                    `json:"compactTokens,omitempty"`
	MaxConcurrent int                    `json:"maxConcurrent,omitempty"`
}

// Profile operations take the profile lock, then the store transaction. The post-commit
// wake hook calls non-blocking Kick, then takes wakeMu. wakeMu is a leaf lock.
// No box IO runs while the profile lock is held.
type ProfileConfig struct {
	mu      sync.RWMutex
	path    string
	entries map[contract.ProfileName]profile
}

// LoadProfiles validates configuration at the file boundary.
func LoadProfiles(path string) (*ProfileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// Legacy shim: accept the former evaluation switch until the upgrade floor permits its removal.
	var decoded map[contract.ProfileName]struct {
		profile
		AgentSurface string `json:"agentSurface"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	profiles := make(map[contract.ProfileName]profile, len(decoded))
	for name, entry := range decoded {
		profiles[name] = entry.profile
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profiles: no profiles")
	}
	if err := validateProfiles(profiles); err != nil {
		return nil, err
	}
	return &ProfileConfig{path: path, entries: profiles}, nil
}

func validateProfiles(profiles map[contract.ProfileName]profile) error {
	for name, p := range profiles {
		for _, rating := range []int{p.Taste, p.Diagnostics, p.Intelligence, p.Cost} {
			if rating < 0 || rating > 10 {
				return fmt.Errorf("profile %q: ratings must be 0..10", name)
			}
		}
		if p.MaxConcurrent < 0 {
			return fmt.Errorf("profile %q: limits must be non-negative", name)
		}
		if p.CompactTokens < 0 {
			return fmt.Errorf("profile %q: compactTokens must be non-negative", name)
		}
		if name == "" || p.Model == "" || !slices.Contains(p.AllowedModels, p.Model) {
			return fmt.Errorf("profile %q: model must be allowed", name)
		}
		switch p.Provider {
		case contract.ModelProviderAnthropic, contract.ModelProviderOpenAI:
		default:
			return fmt.Errorf("profile %q: invalid provider", name)
		}
		switch p.Thinking {
		case contract.ThinkingOff, contract.ThinkingMinimal, contract.ThinkingLow, contract.ThinkingMedium, contract.ThinkingHigh, contract.ThinkingXHigh:
		default:
			return fmt.Errorf("profile %q: invalid thinking level", name)
		}
	}
	return nil
}

func (p *ProfileConfig) List() []contract.ProfileSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]contract.ProfileSummary, 0, len(p.entries))
	for name, profile := range p.entries {
		result = append(result, contract.ProfileSummary{Name: name, Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, Description: profile.Description, Taste: profile.Taste, Diagnostics: profile.Diagnostics, Intelligence: profile.Intelligence, Cost: profile.Cost, AllowedModels: slices.Clone(profile.AllowedModels), CompactTokens: profile.CompactTokens, MaxConcurrent: profile.MaxConcurrent})
	}
	slices.SortFunc(result, func(a, b contract.ProfileSummary) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return result
}

func (s *Service) ProfileList(ctx context.Context, who contract.Principal, _ contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ProfileListResponse{}, forbidden()
	}
	counts, err := s.store.ProfileUsage(ctx)
	if err != nil {
		return contract.ProfileListResponse{}, failure(err)
	}
	profiles := s.profiles.List()
	for i := range profiles {
		profiles[i].InUse = counts[profiles[i].Name]
	}
	return contract.ProfileListResponse{Profiles: profiles}, nil
}

// AllowedModels returns the models a session may call: its profile's, and the ones its last turns ran on. A session
// that changed profile needs the old model until it has moved: its running turn keeps that model, and its next turn
// compacts on it. The old model stays reachable after its profile is removed.
func (s *Service) AllowedModels(ctx context.Context, session store.Session) ([]string, error) {
	_, allowed, err := s.profiles.Model(session.Profile)
	if err != nil {
		return nil, err
	}
	recent, err := s.store.RecentTurnModels(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	return append(allowed, recent...), nil
}

func (p *ProfileConfig) CompactionTokens(name contract.ProfileName) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entries[name].CompactTokens
}

func (p *ProfileConfig) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	profile, ok := p.entries[name]
	if !ok {
		return contract.ModelChoice{}, nil, fmt.Errorf("unknown profile %q", name)
	}
	return contract.ModelChoice{Provider: profile.Provider, ID: profile.Model, Thinking: profile.Thinking}, slices.Clone(profile.AllowedModels), nil
}

func (p *ProfileConfig) update(name contract.ProfileName, value *profile, create bool) *contract.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.updateLocked(name, value, create)
}

func (p *ProfileConfig) updateLocked(name contract.ProfileName, value *profile, create bool) *contract.Error {
	if _, exists := p.entries[name]; create && exists {
		return &contract.Error{Code: contract.ErrProfileExists, Message: "profile already exists"}
	}
	next := make(map[contract.ProfileName]profile, len(p.entries)+1)
	for k, v := range p.entries {
		next[k] = v
	}
	if value == nil {
		if _, ok := next[name]; !ok {
			return &contract.Error{Code: contract.ErrNotFound, Message: "profile not found"}
		}
		delete(next, name)
	} else {
		next[name] = *value
	}
	if len(next) == 0 {
		return invalid("at least one profile is required")
	}
	if err := validateProfiles(next); err != nil {
		return invalid(err.Error())
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return failure(err)
	}
	file, err := os.CreateTemp(filepath.Dir(p.path), ".profiles-*")
	if err != nil {
		return failure(err)
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), p.path)
	}
	if err != nil {
		return failure(err)
	}
	p.entries = next
	return nil
}

func (s *Service) ProfileSet(_ context.Context, who contract.Principal, req contract.ProfileSetRequest) (contract.ProfileSetResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ProfileSetResponse{}, forbidden()
	}
	p, ok := s.profiles.(*ProfileConfig)
	if !ok {
		return contract.ProfileSetResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "profile configuration is read-only"}
	}
	v := req.Profile
	err := p.update(v.Name, &profile{Description: v.Description, Taste: v.Taste, Diagnostics: v.Diagnostics, Intelligence: v.Intelligence, Cost: v.Cost, Provider: v.Provider, Model: v.Model, AllowedModels: slices.Clone(v.AllowedModels), Thinking: v.Thinking, CompactTokens: v.CompactTokens, MaxConcurrent: v.MaxConcurrent}, req.Create)
	return contract.ProfileSetResponse{}, err
}

// WithProfile protects a profile from removal while fn commits a session write.
func (p *ProfileConfig) WithProfile(name contract.ProfileName, fn func() *contract.Error) *contract.Error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if _, ok := p.entries[name]; !ok {
		return invalid("unknown profile")
	}
	return fn()
}

// Remove serializes the in-use check and removal against profile assignments.
func (p *ProfileConfig) Remove(name contract.ProfileName, inUse func() (bool, error)) *contract.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	used, err := inUse()
	if err != nil {
		return failure(err)
	}
	if used {
		return &contract.Error{Code: contract.ErrConflict, Message: "profile is used by an open session"}
	}
	return p.updateLocked(name, nil, false)
}

// Replace moves every open session off name, then removes it. Assignments hold the read lock until they commit, so
// none can land on name between the move and the removal. If the removal fails the sessions stay moved, and the
// profile can be removed again.
func (p *ProfileConfig) Replace(name, replacement contract.ProfileName, move func() error) *contract.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.entries[name]; !ok {
		return &contract.Error{Code: contract.ErrNotFound, Message: "profile not found"}
	}
	if _, ok := p.entries[replacement]; !ok || replacement == name {
		return invalid("replacement must be another profile")
	}
	if err := move(); err != nil {
		return failure(err)
	}
	return p.updateLocked(name, nil, false)
}

func (s *Service) withProfile(name contract.ProfileName, fn func() *contract.Error) *contract.Error {
	if p, ok := s.profiles.(*ProfileConfig); ok {
		return p.WithProfile(name, fn)
	}
	if _, _, err := s.profiles.Model(name); err != nil {
		return invalid("unknown profile")
	}
	return fn()
}

func (s *Service) ProfileRemove(ctx context.Context, who contract.Principal, req contract.ProfileRemoveRequest) (contract.ProfileRemoveResponse, *contract.Error) {
	if who.Kind != contract.PrincipalOwner {
		return contract.ProfileRemoveResponse{}, forbidden()
	}
	p, ok := s.profiles.(*ProfileConfig)
	if !ok {
		return contract.ProfileRemoveResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "profile configuration is read-only"}
	}
	if req.Replacement != "" {
		return contract.ProfileRemoveResponse{}, p.Replace(req.Name, req.Replacement, func() error {
			sessions, err := s.store.SessionsOnProfile(ctx, req.Name)
			if err != nil {
				return err
			}
			moves := make([]store.ProfileMove, 0, len(sessions))
			for _, session := range sessions {
				event, err := s.event(session.Room, who, contract.SessionProfileChanged{Session: session.ID, Profile: req.Replacement})
				if err != nil {
					return err
				}
				moves = append(moves, store.ProfileMove{Session: session.ID, Event: event})
			}
			return s.store.MoveProfile(ctx, req.Replacement, moves)
		})
	}
	return contract.ProfileRemoveResponse{}, p.Remove(req.Name, func() (bool, error) {
		return s.store.ProfileInUse(ctx, req.Name)
	})
}
