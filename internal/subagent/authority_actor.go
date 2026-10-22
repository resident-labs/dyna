package subagent

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
)

// AuthorityActor uses stored session identity and room membership for callers
// in subagent mode. Other process roles need no session lookup. The actor is
// the caller on ctx (authority.Of): in the engine, a call's caller, and no
// role when ctx has none, so the check refuses; outside the engine, this
// process's own actor.
func (m *Manager) AuthorityActor(ctx context.Context) (authority.Actor, error) {
	actor := authority.Of(ctx)
	if actor.Role != authority.Session {
		return actor, nil
	}
	actor, meta, err := m.store().sessionActor(actor.Session)
	if err != nil {
		return authority.Actor{}, err
	}
	stamp, _ := mode.StampFromEnv()
	for _, ref := range []string{stamp.RoomID, stamp.Room} {
		if ref != "" && ref != meta.RoomID && ref != meta.Room {
			return authority.Actor{}, fmt.Errorf("session %s room stamp %q does not match stored room %q", meta.ID, ref, meta.Room)
		}
	}
	return actor, nil
}

// SessionActor is the actor of an open session from its stored role and
// room: the room's root seat is Root and a steward is Steward.
func (s *Store) SessionActor(id string) (authority.Actor, error) {
	actor, _, err := s.sessionActor(id)
	return actor, err
}

func (s *Store) sessionActor(id string) (authority.Actor, Meta, error) {
	meta, err := s.ReadMeta(id)
	if err != nil {
		return authority.Actor{}, Meta{}, err
	}
	if !meta.Closed.IsZero() {
		return authority.Actor{}, Meta{}, fmt.Errorf("session %s is closed", meta.ID)
	}
	actor := authority.Actor{Role: authority.Session, Session: meta.ID, Room: meta.RoomID}
	switch meta.Role {
	case RoleRoot:
		actor.Role = authority.Root
	case RoleSteward:
		actor.Role = authority.Steward
	}
	return actor, meta, nil
}
