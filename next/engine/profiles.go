package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/resident-labs/dyna/box"
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
	Images        bool                   `json:"images"`
	AllowedModels []string               `json:"allowedModels"`
	Thinking      contract.ThinkingLevel `json:"thinking"`
	CompactTokens int                    `json:"compactTokens,omitempty"`
	MaxConcurrent int                    `json:"maxConcurrent,omitempty"`
	Harness       contract.Harness       `json:"harness,omitempty"`
}

func (p profile) modelChoice() contract.ModelChoice {
	return contract.ModelChoice{Provider: p.Provider, ID: p.Model, Thinking: p.Thinking}
}

// Profile operations take the profile lock, then the store transaction. The post-commit
// wake hook calls non-blocking Kick, then takes wakeMu. wakeMu is a leaf lock.
// No box IO runs while the profile lock is held.
type ProfileConfig struct {
	mu       sync.RWMutex
	path     string
	entries  map[contract.ProfileName]profile
	textOnly []contract.TextOnlyModel
}

// LoadProfiles validates configuration at the file boundary.
func LoadProfiles(path string) (*ProfileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var raw map[contract.ProfileName]json.RawMessage
	if err := json.NewDecoder(file).Decode(&raw); err != nil {
		return nil, err
	}
	profiles := make(map[contract.ProfileName]profile, len(raw))
	for name, data := range raw {
		p := profile{Images: true, Harness: contract.HarnessPig}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return nil, fmt.Errorf("profile %q: %w", name, err)
		}
		if slices.ContainsFunc(append(slices.Clone(p.AllowedModels), p.Model), func(id string) bool { return strings.HasPrefix(id, "antigravity/") }) {
			log.Printf("profile %q names an antigravity/ model, which no proxy account serves any more; move it to a profile with harness agy", name)
		}
		profiles[name] = p
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profiles: no profiles")
	}
	if err := validateProfiles(profiles); err != nil {
		return nil, err
	}
	return &ProfileConfig{path: path, entries: profiles, textOnly: textOnlyModels(profiles)}, nil
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
		switch p.Harness {
		case contract.HarnessPig:
			switch p.Provider {
			case contract.ModelProviderAnthropic, contract.ModelProviderOpenAI:
			default:
				return fmt.Errorf("profile %q: invalid provider", name)
			}
		case contract.HarnessAgy:
			if p.Provider != contract.ModelProviderGoogle {
				return fmt.Errorf("profile %q: an agy profile has provider google", name)
			}
			if p.MaxConcurrent == 0 {
				return fmt.Errorf("profile %q: an agy profile needs max-concurrent above 0, because one Google account serves every agy session", name)
			}
		default:
			return fmt.Errorf("profile %q: harness is pig or agy", name)
		}
		switch p.Thinking {
		case contract.ThinkingOff, contract.ThinkingMinimal, contract.ThinkingLow, contract.ThinkingMedium, contract.ThinkingHigh, contract.ThinkingXHigh, contract.ThinkingMax:
		default:
			return fmt.Errorf("profile %q: invalid thinking level", name)
		}
	}
	return nil
}

func (p *ProfileConfig) List() []contract.Profile {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]contract.Profile, 0, len(p.entries))
	for name, profile := range p.entries {
		result = append(result, contract.Profile{Name: name, Provider: profile.Provider, Model: profile.Model, Images: &profile.Images, Thinking: profile.Thinking, Description: profile.Description, Taste: profile.Taste, Diagnostics: profile.Diagnostics, Intelligence: profile.Intelligence, Cost: profile.Cost, AllowedModels: slices.Clone(profile.AllowedModels), CompactTokens: profile.CompactTokens, MaxConcurrent: profile.MaxConcurrent, Harness: profile.Harness})
	}
	slices.SortFunc(result, func(a, b contract.Profile) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return result
}

// textOnlyModels lists the models a box defines in its models.json: every model of a profile without images, and each model
// of a profile with images that Pi's catalog lacks, such as one released after the pinned Pi, marked Images. A model some
// profile declares text-only stays text-only.
func textOnlyModels(profiles map[contract.ProfileName]profile) []contract.TextOnlyModel {
	images := map[contract.TextOnlyModel]bool{}
	for _, p := range profiles {
		if p.Harness == contract.HarnessAgy {
			continue
		}
		for _, id := range append(slices.Clone(p.AllowedModels), p.Model) {
			model := contract.TextOnlyModel{Provider: p.Provider, Model: id}
			switch seen, ok := images[model]; {
			case !p.Images:
				images[model] = false
			case !ok:
				if _, catalogued := ai.LookupModelExact(string(p.Provider) + "/" + id); !catalogued {
					images[model] = true
				}
			default:
				images[model] = seen
			}
		}
	}
	result := make([]contract.TextOnlyModel, 0, len(images))
	for model, takesImages := range images {
		model.Images = takesImages
		result = append(result, model)
	}
	slices.SortFunc(result, func(a, b contract.TextOnlyModel) int {
		if n := strings.Compare(string(a.Provider), string(b.Provider)); n != 0 {
			return n
		}
		return strings.Compare(a.Model, b.Model)
	})
	return result
}

func (p *ProfileConfig) TextOnlyModels() []contract.TextOnlyModel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.textOnly)
}

func (s *Service) ProfileList(ctx context.Context, who contract.Principal, _ contract.ProfileListRequest) (contract.ProfileListResponse, *contract.Error) {
	counts, err := s.store.ProfileUsage(ctx)
	if err != nil {
		return contract.ProfileListResponse{}, failure(err)
	}
	stored := s.profiles.List()
	entries := make([]contract.ProfileEntry, len(stored))
	for i, p := range stored {
		entries[i] = contract.ProfileEntry{Profile: p, InUse: counts[p.Name]}
	}
	return contract.ProfileListResponse{Profiles: entries}, nil
}

// AllowedModels returns the models a session may call: its profile's, and the ones its last turns ran on. A session
// that changed profile needs the old model until it has moved: its running turn keeps that model, and its next turn
// compacts on it. The old model stays reachable after its profile is removed. A crew may also call every model of its
// script's profiles, and the second result says the session is one.
func (s *Service) AllowedModels(ctx context.Context, session store.Session) ([]string, bool, error) {
	_, allowed, err := s.profiles.Model(session.Profile)
	if err != nil {
		return nil, false, err
	}
	run, err := s.store.SessionWorkflow(ctx, session.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	crew := err == nil
	for _, name := range run.Profiles {
		// A profile removed during a run has no models left, so its workers fail like any refused call.
		if _, models, err := s.profiles.Model(name); err == nil {
			allowed = append(allowed, models...)
		}
	}
	// Root's advisor runs in root's box and calls the model through root's route.
	if session.Root {
		advisor, err := s.store.RoomAdvisorProfile(ctx, session.Room)
		if err != nil {
			return nil, false, err
		}
		if advisor != "" {
			if _, models, err := s.profiles.Model(advisor); err == nil {
				allowed = append(allowed, models...)
			}
		}
	}
	recent, err := s.store.RecentTurnModels(ctx, session.ID)
	if err != nil {
		return nil, false, err
	}
	return append(allowed, recent...), crew, nil
}

func (p *ProfileConfig) CompactionTokens(name contract.ProfileName) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entries[name].CompactTokens
}

// Harness is the harness a profile runs on; an unknown profile reads as pig, and its caller's own unknown-profile check refuses it.
func (p *ProfileConfig) Harness(name contract.ProfileName) contract.Harness {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if profile, ok := p.entries[name]; ok {
		return profile.Harness
	}
	return contract.HarnessPig
}

func (p *ProfileConfig) MaxConcurrent(name contract.ProfileName) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.entries[name].MaxConcurrent
}

func (p *ProfileConfig) Model(name contract.ProfileName) (contract.ModelChoice, []string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	profile, ok := p.entries[name]
	if !ok {
		names := make([]string, 0, len(p.entries))
		for known := range p.entries {
			names = append(names, string(known))
		}
		slices.Sort(names)
		return contract.ModelChoice{}, nil, fmt.Errorf("unknown profile %q; the profiles are %s", name, strings.Join(names, ", "))
	}
	return profile.modelChoice(), slices.Clone(profile.AllowedModels), nil
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
	p.textOnly = textOnlyModels(next)
	return nil
}

func (s *Service) ProfileSet(ctx context.Context, who contract.Principal, req contract.ProfileSetRequest) (contract.ProfileSetResponse, *contract.Error) {
	p, ok := s.profiles.(*ProfileConfig)
	if !ok {
		return contract.ProfileSetResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "profile configuration is read-only"}
	}
	v := req.Profile
	if v.Harness == "" {
		v.Harness = contract.HarnessPig
	}
	if slices.ContainsFunc(append(slices.Clone(v.AllowedModels), v.Model), func(id string) bool { return strings.HasPrefix(id, "antigravity/") }) {
		return contract.ProfileSetResponse{}, invalid("antigravity/ models no longer run through the proxy; make a profile with --harness agy and --provider google instead")
	}
	if !req.Create {
		for _, old := range p.List() {
			if old.Name != v.Name || old.Harness == v.Harness {
				continue
			}
			if used, err := s.store.ProfileInUse(ctx, v.Name); err != nil {
				return contract.ProfileSetResponse{}, failure(err)
			} else if used {
				return contract.ProfileSetResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("profile %s runs on %s and open sessions use it, and a session cannot move between pig and agy; make a new profile on %s for new sessions, or close those sessions first", v.Name, old.Harness, v.Harness)}
			}
		}
	}
	images := v.Images == nil || *v.Images
	err := p.update(v.Name, &profile{Description: v.Description, Taste: v.Taste, Diagnostics: v.Diagnostics, Intelligence: v.Intelligence, Cost: v.Cost, Provider: v.Provider, Model: v.Model, Images: images, AllowedModels: slices.Clone(v.AllowedModels), Thinking: v.Thinking, CompactTokens: v.CompactTokens, MaxConcurrent: v.MaxConcurrent, Harness: v.Harness}, req.Create)
	if err == nil {
		s.wakeWorkflowsOnProfile(ctx, v.Name)
		if !req.Create {
			// An edit can move every session on the profile to another model at its next turn.
			if sessions, readErr := s.store.SessionsOnProfile(ctx, v.Name); readErr == nil {
				s.sessionsMoved(ctx, sessions, v.Name)
			}
		}
	}
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
	if p.entries[name].Harness != p.entries[replacement].Harness {
		return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("profile %s runs on %s and %s on %s, and a session cannot move between pig and agy; name a replacement on the same harness", name, p.entries[name].Harness, replacement, p.entries[replacement].Harness)}
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
	p, ok := s.profiles.(*ProfileConfig)
	if !ok {
		return contract.ProfileRemoveResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "profile configuration is read-only"}
	}
	if req.Replacement != "" {
		var moved []store.Session
		problem := p.Replace(req.Name, req.Replacement, func() error {
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
			if err := s.store.MoveProfile(ctx, req.Replacement, moves); err != nil {
				return err
			}
			moved = sessions
			return nil
		})
		if problem == nil {
			s.wakeWorkflowsOnProfile(ctx, req.Name)
		}
		// Replace holds the profile lock while it moves, and this takes that lock and reads the proxy.
		s.sessionsMoved(ctx, moved, req.Replacement)
		return contract.ProfileRemoveResponse{}, problem
	}
	problem := p.Remove(req.Name, func() (bool, error) {
		return s.store.ProfileInUse(ctx, req.Name)
	})
	if problem == nil {
		s.wakeWorkflowsOnProfile(ctx, req.Name)
	}
	return contract.ProfileRemoveResponse{}, problem
}

// wakeWorkflowsOnProfile wakes the box streams, which send a reload if the crew's profiles changed.
func (s *Service) wakeWorkflowsOnProfile(ctx context.Context, name contract.ProfileName) {
	sessions, err := s.store.WorkflowsOnProfile(context.WithoutCancel(ctx), name)
	if err != nil {
		log.Printf("wake workflows on profile %q: %v", name, err)
		return
	}
	s.live.Wake(sessions, nil)
}

// agyHost is the one host agy sessions run on: the owner signs in to Google there, and one IP uses the login.
const agyHost contract.HostName = "hermeslab"

// pigOnly refuses an agy profile where only PiG runs: a workflow's workers and the room's advisor.
func (s *Service) pigOnly(name contract.ProfileName, what string) *contract.Error {
	if s.profiles.Harness(name) == contract.HarnessAgy {
		return invalid(fmt.Sprintf("profile %s runs on agy, and %s run only on PiG profiles; pick a profile whose harness is pig", name, what))
	}
	return nil
}

// agyPlacement pins a session on an agy profile to agyHost. host is what the caller asked for: empty, or a host the session may not take.
func (s *Service) agyPlacement(profile contract.ProfileName, host contract.HostName, sameHost bool) (contract.HostName, *contract.Error) {
	if s.profiles.Harness(profile) != contract.HarnessAgy {
		return host, nil
	}
	if sameHost || host != "" && host != agyHost {
		return "", invalid(fmt.Sprintf("profile %s runs on agy, which runs only on host %s, where the owner signed in to Google; leave host and sameHost out, or pick a pig profile for another host", profile, agyHost))
	}
	return agyHost, nil
}

// The agy program and the signed-in profile directory are mounted into the box by the host's root, the directory read-write.
// Dyna never reads or copies a file in it.
var agyMount = contract.BoxAgy{Binary: box.AgyBinary, GeminiDir: contract.AgyGeminiDir}

// AgyProfile says a profile runs on the agy harness.
func (s *Service) AgyProfile(name contract.ProfileName) bool {
	return s.profiles.Harness(name) == contract.HarnessAgy
}

// agyAccount is what one Google account allows when name is an agy profile, and nothing otherwise: every agy profile shares the
// account, so their open sessions share one limit, the smallest max-concurrent among them. Call it inside withProfile, so that a
// profile added or edited meanwhile cannot slip past the count. A pig profile's max-concurrent bounds workflow workers only.
func (s *Service) agyAccount(name contract.ProfileName) ([]contract.ProfileName, int) {
	if p, ok := s.profiles.(*ProfileConfig); ok {
		return p.agyAccountLocked(name)
	}
	return nil, 0
}

// agyAccountLocked is agyAccount for a caller that holds p.mu.
func (p *ProfileConfig) agyAccountLocked(name contract.ProfileName) ([]contract.ProfileName, int) {
	if p.entries[name].Harness != contract.HarnessAgy {
		return nil, 0
	}
	var names []contract.ProfileName
	limit := math.MaxInt
	for profile, entry := range p.entries {
		if entry.Harness == contract.HarnessAgy {
			names = append(names, profile)
			limit = min(limit, entry.MaxConcurrent)
		}
	}
	return names, limit
}

// checkHarnessSwitch refuses a live session's move between pig and agy that cannot be made, with the next step. The session keeps
// its volume, so its earlier files carry over to the new harness as a handoff; what cannot move is the host, and a workflow's crew.
func (s *Service) checkHarnessSwitch(ctx context.Context, target store.Session, profile contract.ProfileName) *contract.Error {
	if _, err := s.store.SessionWorkflow(ctx, target.ID); err == nil {
		return invalid(fmt.Sprintf("session %s is a workflow's crew, and a crew runs only on PiG profiles; a profile whose harness is pig is the only one it can use", target.Name))
	} else if !errors.Is(err, store.ErrNotFound) {
		return failure(err)
	}
	current, err := s.store.Box(ctx, target.Box)
	if err != nil {
		return failure(err)
	}
	if s.profiles.Harness(profile) == contract.HarnessAgy && current.Host != agyHost {
		return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("session %s runs on host %q and agy runs only on host %s, where the owner signed in to Google; its files cannot move there, so start a new session on profile %s", target.Name, current.Host, agyHost, profile)}
	}
	if can, err := s.store.BoxCanHandoff(ctx, target.Box); err != nil {
		return failure(err)
	} else if !can {
		return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("session %s runs in a box that cannot hand its conversation to another harness yet; the engine and box binaries differ until the box restarts, so try again after its box has restarted, or start a new session on profile %s", target.Name, profile)}
	}
	return nil
}
