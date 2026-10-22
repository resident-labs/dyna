package goalkeeper

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/subagent"
)

// SessionBackend keeps provider processes out of scheduler tests.
type SessionBackend interface {
	Profiles() ([]profile.Profile, error)
	Inspect(string) (subagent.Info, subagent.Turn, error)
	Named(string) (subagent.Info, error)
	// Start and Continue send task with a result schema; Continue sends it as
	// a follow-up turn of an idle session.
	Start(ctx context.Context, roomID, profile, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error)
	Continue(ctx context.Context, id, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error)
	Wait(context.Context, string, int) (subagent.Info, subagent.Turn, error)
	Close(context.Context, string) error
	Failures(time.Time) (map[string]time.Time, error)
}

type liveSessions struct{ service *Service }

func (b liveSessions) Profiles() ([]profile.Profile, error) {
	p, err := b.service.Notify.Sessions.ProfileStore()
	if err != nil {
		return nil, err
	}
	return p.Profiles, nil
}
func (b liveSessions) Inspect(id string) (subagent.Info, subagent.Turn, error) {
	m := b.service.Notify.Sessions
	info, err := m.Store.Resolve(id)
	if err != nil {
		return info, subagent.Turn{}, err
	}
	t, err := m.Store.ReadTurn(info.ID, info.Turns)
	return info, t, err
}
func (b liveSessions) Named(roomID string) (subagent.Info, error) {
	rec, err := b.service.Notify.Rooms.Resolve(roomID)
	if err != nil {
		return subagent.Info{}, err
	}
	return b.service.Notify.Sessions.Store.Resolve(rec.Name + ":keeper")
}
func (b liveSessions) Start(ctx context.Context, roomID, name, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error) {
	if err := ctx.Err(); err != nil {
		return subagent.Info{}, subagent.Turn{}, err
	}
	dir := filepath.Join(b.service.Store.Root, "rooms", stableID(roomID))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return subagent.Info{}, subagent.Turn{}, err
	}
	return b.service.Notify.Sessions.StartForEngine(subagent.StartOptions{Service: true, Role: "keeper", Name: "keeper", Profile: name, Room: roomID, Dir: dir,
		Goal: "Judge the room goals in each keeper assignment and return a valid result; done when Dyna closes this keeper session.", Task: task, Schema: schema, Deadline: deadline})
}
func (b liveSessions) Continue(ctx context.Context, id, task, schema string, deadline time.Time) (subagent.Info, subagent.Turn, error) {
	m := b.service.Notify.Sessions
	turn, err := m.Assign(ctx, id, task, schema, deadline)
	if err != nil {
		return subagent.Info{}, turn, err
	}
	info, err := m.Store.Resolve(id)
	return info, turn, err
}
func (b liveSessions) Wait(ctx context.Context, id string, n int) (subagent.Info, subagent.Turn, error) {
	return b.service.Notify.Sessions.Wait(ctx, id, n)
}
func (b liveSessions) Close(ctx context.Context, id string) error {
	_, err := b.service.Notify.Sessions.CloseWithOptions(ctx, id, subagent.CloseOptions{Force: true})
	return err
}
func (b liveSessions) Failures(now time.Time) (map[string]time.Time, error) {
	return rolesession.RecentProfileFailures(b.service.Notify.Sessions.Store, now)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) keeperState(roomID string) (goals.KeeperRoomState, error) {
	st, err := s.Store.ReadOpen()
	if err != nil {
		return goals.KeeperRoomState{}, err
	}
	return st.Room(roomID).Keeper, nil
}
func (s *Service) backend() SessionBackend {
	if s.Sessions != nil {
		return s.Sessions
	}
	return liveSessions{s}
}

// KeeperProfile returns the profile the room's next keeper turn uses and why.
func (s *Service) KeeperProfile(roomID, pinned string) (string, string, error) {
	cfg, err := s.keeperState(roomID)
	if err != nil {
		return "", "", err
	}
	backend := s.backend()
	profiles, err := backend.Profiles()
	if err != nil {
		return "", "", err
	}
	recent, err := backend.Failures(s.now())
	if err != nil {
		return "", "", err
	}
	return chooseKeeperProfile(profiles, pinned, rolesession.MergeFailures(cfg.FailedProfiles, recent), s.now())
}
func chooseKeeperProfile(profiles []profile.Profile, pinned string, failures map[string]time.Time, now time.Time) (string, string, error) {
	eligible := make([]profile.Profile, 0, len(profiles))
	for _, p := range profiles {
		constrained, err := subagent.KeeperProfile(p)
		if err != nil {
			if p.Name == pinned {
				return "", "", fmt.Errorf("keeper profile %q: %w", p.Name, err)
			}
			continue
		}
		eligible = append(eligible, constrained)
	}
	return rolesession.ChooseProfile(eligible, pinned, failures, now, "keeper")
}

func (s *Service) saveKeeper(roomID string, cfg goals.KeeperRoomState) error {
	return s.Store.UpdateOpen(func(st *goals.State) error { st.Room(roomID).Keeper = cfg; return nil })
}

func assignmentMarker(id string) string { return "[KEEPER ASSIGNMENT " + id + "]" }
