package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	images := v.Images == nil || *v.Images
	// A change of harness is refused while sessions use the profile, checked under the registry's write lock: an assignment holds the
	// read lock from its decision to its commit, so none can land on the old harness meanwhile.
	err := p.Set(v.Name, &profile{Description: v.Description, Taste: v.Taste, Diagnostics: v.Diagnostics, Intelligence: v.Intelligence, Cost: v.Cost, Provider: v.Provider, Model: v.Model, Images: images, AllowedModels: slices.Clone(v.AllowedModels), Thinking: v.Thinking, CompactTokens: v.CompactTokens, MaxConcurrent: v.MaxConcurrent, Harness: v.Harness}, req.Create, func(old profile) *contract.Error {
		if old.Harness == v.Harness {
			return nil
		}
		if used, err := s.store.ProfileInUse(ctx, v.Name); err != nil {
			return failure(err)
		} else if used {
			return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("profile %s runs on %s and open sessions use it, and a session cannot move between pig and agy; make a new profile on %s for new sessions, or close those sessions first", v.Name, old.Harness, v.Harness)}
		}
		return nil
	})
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

// Set writes a profile after check, which sees the profile it replaces, under the write lock. check is not called for a new profile.
func (p *ProfileConfig) Set(name contract.ProfileName, value *profile, create bool, check func(old profile) *contract.Error) *contract.Error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.entries[name]; ok && !create {
		if problem := check(old); problem != nil {
			return problem
		}
	}
	return p.updateLocked(name, value, create)
}

// WithProfile protects a profile from removal while fn commits a session write.
func (p *ProfileConfig) WithProfile(name contract.ProfileName, fn func(harness func(contract.ProfileName) contract.Harness) *contract.Error) *contract.Error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if _, ok := p.entries[name]; !ok {
		return invalid("unknown profile")
	}
	// fn reads harnesses through the held lock: taking it again would block behind a queued edit, which waits for fn.
	return fn(func(name contract.ProfileName) contract.Harness {
		if profile, ok := p.entries[name]; ok {
			return profile.Harness
		}
		return contract.HarnessPig
	})
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

func (s *Service) withProfile(name contract.ProfileName, fn func(harness func(contract.ProfileName) contract.Harness) *contract.Error) *contract.Error {
	if p, ok := s.profiles.(*ProfileConfig); ok {
		return p.WithProfile(name, fn)
	}
	if _, _, err := s.profiles.Model(name); err != nil {
		return invalid("unknown profile")
	}
	return fn(s.profiles.Harness)
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

// pigOnly refuses an agy profile where only PiG runs: a workflow's workers and the room's advisor.
func (s *Service) pigOnly(name contract.ProfileName, what string) *contract.Error {
	if s.profiles.Harness(name) == contract.HarnessAgy {
		return invalid(fmt.Sprintf("profile %s runs on agy, and %s run only on PiG profiles; pick a profile whose harness is pig", name, what))
	}
	return nil
}

// AgySource is the host the owner signed in to Google on: `dyna agy seed` copies its program and account folder to another host, as the
// owner's choice. It says where the login came from, not where agy may run.
const AgySource contract.HostName = "hermeslab"

// agyPlacement places a session on an agy profile: on a host that reports agy installed and signed in. host is what the caller asked for,
// or empty for the placement to choose among the eligible hosts that have it; sameHost leaves the check to the parent's host.
func (s *Service) agyPlacement(ctx context.Context, room contract.RoomID, profile contract.ProfileName, host contract.HostName, sameHost bool) (contract.HostName, *contract.Error) {
	if s.profiles.Harness(profile) != contract.HarnessAgy {
		return host, nil
	}
	if host != "" {
		return host, s.agyHostProblem(ctx, profile, host)
	}
	if sameHost {
		return "", nil
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return "", failure(err)
	}
	for _, h := range hosts {
		if h.Agy && s.validateSessionHost(ctx, room, h.Name) == nil {
			return "", nil
		}
	}
	return "", invalid(fmt.Sprintf("profile %s runs on agy, and no host that takes this room's sessions has agy installed and signed in; host list shows each host's agy, and the owner signs in to Antigravity on a host", profile))
}

// agyHostProblem refuses a host that does not report agy installed and signed in, with the next step.
func (s *Service) agyHostProblem(ctx context.Context, profile contract.ProfileName, name contract.HostName) *contract.Error {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return failure(err)
	}
	if slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == name && h.Agy }) {
		return nil
	}
	return invalid(fmt.Sprintf("profile %s runs on agy, and host %s does not have agy installed and signed in; host list shows which hosts do, and the owner signs in to Antigravity on %s", profile, name, name))
}

// The agy program and the signed-in profile directory are mounted into the box by the host's root, the directory read-write.
// Dyna never reads or copies a file in it.
var agyMount = contract.BoxAgy{Binary: box.AgyBinary, GeminiDir: contract.AgyGeminiDir, Exit: contract.AgyExitPath}

// AgyExitAllowed says a box may use the agy exit: the harness it reported at its last hello is agy. That is stored, not asked of the
// request, so no other box, whatever its session says, reaches Google through the engine.
func (s *Service) AgyExitAllowed(ctx context.Context, id contract.BoxID) (bool, error) {
	harness, err := s.store.BoxHarness(ctx, id)
	return harness == contract.HarnessAgy, err
}

// AgyProfile says a profile runs on the agy harness.
func (s *Service) AgyProfile(name contract.ProfileName) bool {
	return s.profiles.Harness(name) == contract.HarnessAgy
}

// checkHarnessSwitch refuses a live session's move between pig and agy that cannot be made, with the next step. The session keeps
// its volume, so its earlier files carry over to the new harness as a handoff; what cannot move is the host, and a workflow's crew.
func (s *Service) checkHarnessSwitch(ctx context.Context, target store.Session, profile contract.ProfileName, to contract.Harness) *contract.Error {
	if _, err := s.store.SessionWorkflow(ctx, target.ID); err == nil {
		return invalid(fmt.Sprintf("session %s is a workflow's crew, and a crew runs only on PiG profiles; a profile whose harness is pig is the only one it can use", target.Name))
	} else if !errors.Is(err, store.ErrNotFound) {
		return failure(err)
	}
	current, err := s.store.Box(ctx, target.Box)
	if err != nil {
		return failure(err)
	}
	if to == contract.HarnessAgy {
		if current.Host == "" {
			return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("session %s's box has no host yet, and agy needs the host its files are on; try again once its box has started", target.Name)}
		}
		if problem := s.agyHostProblem(ctx, profile, current.Host); problem != nil {
			problem.Code = contract.ErrConflict
			problem.Message = fmt.Sprintf("session %s runs on host %q, which does not have agy installed and signed in, and its files cannot move to another host; the owner can sign in to Antigravity on %s, or start a new session on profile %s", target.Name, current.Host, current.Host, profile)
			return problem
		}
	}
	if can, err := s.store.BoxCanHandoff(ctx, target.Box); err != nil {
		return failure(err)
	} else if !can {
		return &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("session %s runs in a box that cannot hand its conversation to another harness yet; the engine and box binaries differ until the box restarts, so try again after its box has restarted, or start a new session on profile %s", target.Name, profile)}
	}
	return nil
}

// sameHostAgy refuses an agy session beside a parent whose host has no agy.
func (s *Service) sameHostAgy(ctx context.Context, profile contract.ProfileName, parent contract.SessionID) *contract.Error {
	p, err := s.store.Session(ctx, parent)
	if err != nil {
		return failure(err)
	}
	b, err := s.store.Box(ctx, p.Box)
	if err != nil {
		return failure(err)
	}
	return s.agyHostProblem(ctx, profile, b.Host)
}

// AgySeedProblem says why the owner may not seed a host, or nil: only the owner seeds, never the source itself, an exec-only host or one
// with agents off. A host that is seeded already, or that has agy sessions, has an account folder, which the copy refuses to touch.
func (s *Service) AgySeedProblem(ctx context.Context, caller contract.Principal, name contract.HostName) *contract.Error {
	if caller.Kind != contract.PrincipalOwner {
		return forbidden()
	}
	if name == AgySource {
		return invalid(fmt.Sprintf("host %s is where the owner signed in; seed another host from it", name))
	}
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return failure(err)
	}
	index := slices.IndexFunc(hosts, func(h box.Host) bool { return h.Name == name })
	if index < 0 || hosts[index].State == box.HostRemoved {
		return invalid("unknown host: " + string(name))
	}
	if hosts[index].ExecOnly || hosts[index].NoAgents {
		return invalid(fmt.Sprintf("host %s is exec-only or has agents off, so it never runs agy sessions", name))
	}
	return nil
}
