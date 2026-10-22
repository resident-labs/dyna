package steward

import (
	"context"
	"time"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/rolesession"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// Sessions keeps provider processes out of wake tests.
type Sessions interface {
	Profiles() ([]profile.Profile, error)
	Failures(time.Time) (map[string]time.Time, error)
	Inspect(id string) (subagent.Info, subagent.Turn, error)
	// Turn reads one turn of a session.
	Turn(id string, n int) (subagent.Turn, error)
	// Named finds the open merge steward session in a room.
	Named(roomID string) (subagent.Info, error)
	// Start and Assign return the number of the turn that runs task.
	Start(roomID, profile, dir, goal, task string) (subagent.Info, int, error)
	// Assign sends task as a follow-up turn of an idle session.
	Assign(ctx context.Context, id, task string) (int, error)
	Close(ctx context.Context, id string) error
}

// Live uses the session manager in this process.
type Live struct {
	Manager *subagent.Manager
	Rooms   *room.Store
}

func (l Live) Profiles() ([]profile.Profile, error) {
	p, err := l.Manager.ProfileStore()
	if err != nil {
		return nil, err
	}
	return p.Profiles, nil
}
func (l Live) Failures(now time.Time) (map[string]time.Time, error) {
	return rolesession.RecentProfileFailures(l.Manager.Store, now)
}
func (l Live) Inspect(id string) (subagent.Info, subagent.Turn, error) {
	info, err := l.Manager.Store.Resolve(id)
	if err != nil {
		return info, subagent.Turn{}, err
	}
	t, err := l.Manager.Store.ReadTurn(info.ID, info.Turns)
	return info, t, err
}
func (l Live) Turn(id string, n int) (subagent.Turn, error) { return l.Manager.Store.ReadTurn(id, n) }
func (l Live) Named(roomID string) (subagent.Info, error) {
	rec, err := l.Rooms.Resolve(roomID)
	if err != nil {
		return subagent.Info{}, err
	}
	return l.Manager.Store.Resolve(rec.Name + ":" + subagent.RoleSteward)
}
func (l Live) Start(roomID, prof, dir, goal, task string) (subagent.Info, int, error) {
	info, turn, err := l.Manager.StartForEngine(subagent.StartOptions{Service: true, Role: subagent.RoleSteward, Name: subagent.RoleSteward, Profile: prof, Room: roomID, Dir: dir, Goal: goal, Task: task})
	return info, turn.N, err
}
func (l Live) Assign(ctx context.Context, id, task string) (int, error) {
	turn, err := l.Manager.Assign(ctx, id, task, "", time.Time{})
	return turn.N, err
}
func (l Live) Close(ctx context.Context, id string) error {
	_, err := l.Manager.CloseWithOptions(ctx, id, subagent.CloseOptions{Force: true})
	return err
}
