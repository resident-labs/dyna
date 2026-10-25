package roomsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"

	"dyna-agent/internal/subagent"
)

// SessionInRoom resolves a session the owner acts on and refuses one that
// is not in roomRef, so a stale menu can never act on another room.
func (s *Service) SessionInRoom(roomRef, ref string) (subagent.Info, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return subagent.Info{}, err
	}
	info, err := s.sessions().Resolve(ref)
	if err != nil {
		return subagent.Info{}, err
	}
	if info.RoomID != rec.ID {
		return subagent.Info{}, fmt.Errorf("session %s is in room %q, not %q", info.Address(), info.Room, rec.Name)
	}
	return info, nil
}

// actOnSession resolves a session that the caller on ctx changes, and
// checks the caller with checkActsOn.
func (s *Service) actOnSession(ctx context.Context, roomRef, ref string) (subagent.Info, error) {
	info, err := s.SessionInRoom(roomRef, ref)
	if err != nil {
		return subagent.Info{}, err
	}
	return info, s.checkActsOn(ctx, info)
}

// checkActsOn applies the rule the CLI's Target applied in its own process
// before these controls were engine calls: a caller in subagent mode, that
// is a session, may change only itself and its descendants.
// The root seat, which runs in root mode, and the owner may change any
// session in the room.
func (s *Service) checkActsOn(ctx context.Context, info subagent.Info) error {
	actor := authority.Of(ctx)
	if actor.Role != authority.Session {
		return nil
	}
	return s.Notify.Sessions.CheckActsOn(actor.Session, info)
}

// StopSession stops a session's running turn. The session stays open, and
// the next post to it resumes it. Stopping can take up to about 25 seconds
// when a worker ignores SIGTERM; give ctx room for that.
func (s *Service) StopSession(ctx context.Context, roomRef, ref string) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.Stop(ctx, info.ID); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}

// CloseSession closes a session: it stops a running turn first and frees the
// name. It hands work to the nearest open ancestor, or root.
// CloseOptions are the choices a close makes explicit.
type CloseOptions struct {
	Force         bool `json:"force"`
	KeepWorktrees bool `json:"keepWorktrees"`
}

func (s *Service) CloseSession(ctx context.Context, roomRef, ref string) (Member, error) {
	return s.closeSession(ctx, roomRef, ref, CloseOptions{})
}

// ForceCloseSession closes even when an automatic handoff fails.
func (s *Service) ForceCloseSession(ctx context.Context, roomRef, ref string) (Member, error) {
	return s.closeSession(ctx, roomRef, ref, CloseOptions{Force: true})
}

// CloseSessionOptions is close with the CLI's choices: force past failed
// handoffs, and keep the session's worktrees for a manual handoff.
// Only the CLI uses it, so the two plain calls the web app uses stay as they
// are, and the worktree locks stay behind this call's authority check.
func (s *Service) CloseSessionOptions(ctx context.Context, roomRef, ref string, opts CloseOptions) (Member, error) {
	return s.closeSession(ctx, roomRef, ref, opts)
}

func (s *Service) closeSession(ctx context.Context, roomRef, ref string, opts CloseOptions) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	closed, err := s.Notify.Sessions.CloseWithOptions(ctx, info.ID, subagent.CloseOptions{Force: opts.Force, KeepWorktrees: opts.KeepWorktrees})
	if err != nil {
		return Member{}, err
	}
	member, err := s.memberByID(info.ID)
	member.CloseSummary = closed.CloseSummary
	return member, err
}

func (s *Service) memberByID(id string) (Member, error) {
	info, err := s.sessions().Info(id)
	if err != nil {
		return Member{}, err
	}
	m := s.member(info, 0, 0)
	m.Emoji = s.avatars(info.RoomID).byID[info.ID]
	return m, nil
}

// PromoteSession makes an open session a top-level lead in the same room.
func (s *Service) PromoteSession(ctx context.Context, roomRef, ref string) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.Promote(ctx, info.ID); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}

// AdoptSession moves an open session, with its descendants, under parentRef
// in the same room.
func (s *Service) AdoptSession(ctx context.Context, roomRef, ref, parentRef string) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.Adopt(ctx, info.ID, parentRef); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}

// RenameSession gives an open session a new label in its room.
func (s *Service) RenameSession(ctx context.Context, roomRef, ref, name string) (Member, error) {
	info, err := s.actOnSession(ctx, roomRef, ref)
	if err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.Rename(ctx, info.ID, name); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}

// SubagentStart is what dyna subagent start sends. The CLI reads the brief
// and the schema from its files and resolves the room with its own stamp.
// It holds no role and no parent: those come from the caller.
type SubagentStart struct {
	Profile, Name, Dir, Cwd, Schema, Goal, Task string
	// Under names the goal to delegate from; empty picks deterministically.
	Under string
	// Fixes names room issues linked to the session's goal.
	Fixes []int
	// Owner labels an owner caller's harness thread, such as claude:<id>.
	Owner string
	// OwnerDisplay asks for the new session's own shells to use the owner's
	// display. Only an owner or root caller may set it.
	OwnerDisplay bool
}

// StartSubagent starts a session for the caller in the room. A session
// caller becomes the parent, and Start refuses another room.
func (s *Service) StartSubagent(ctx context.Context, roomRef string, in SubagentStart) (subagent.Info, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return subagent.Info{}, err
	}
	info, _, err := s.Notify.Sessions.Start(ctx, subagent.StartOptions{Profile: in.Profile, Name: in.Name, Room: rec.ID, Dir: in.Dir, Cwd: in.Cwd, Schema: in.Schema, Goal: in.Goal, Under: in.Under, Fixes: in.Fixes, Task: in.Task, Owner: in.Owner, OwnerDisplay: in.OwnerDisplay})
	return info, err
}

// ReopenSession restores a closed conversation in the requested room.
func (s *Service) ReopenSession(ctx context.Context, roomRef, ref string) (Member, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return Member{}, err
	}
	info, err := s.sessions().ResolveClosed(ref)
	if err != nil {
		return Member{}, err
	}
	if info.RoomID != rec.ID {
		return Member{}, fmt.Errorf("session %s is in room %q, not %q", info.Address(), info.Room, rec.Name)
	}
	if err := s.checkActsOn(ctx, info); err != nil {
		return Member{}, err
	}
	if _, err := s.Notify.Sessions.Reopen(ctx, info.ID); err != nil {
		return Member{}, err
	}
	return s.memberByID(info.ID)
}
