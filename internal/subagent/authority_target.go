package subagent

import (
	"context"

	"dyna-agent/internal/authority"
)

// authorityTarget checks process roles before loading the target. Session
// relationships use the stored actor ID and target metadata.
func (m *Manager) authorityTarget(ctx context.Context, action, ref string) (Info, error) {
	actor, err := m.AuthorityActor(ctx)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if actor.Role == authority.Session {
		info, err = m.store().Resolve(ref)
		if err != nil {
			return Info{}, err
		}
	}
	err = authority.Check(action, actor, func(relation authority.Relation) bool {
		switch relation {
		case authority.Parent:
			return actor.Session == info.ParentSession
		case authority.Self:
			return actor.Session == info.ID
		case authority.NotSelf:
			return actor.Session != info.ID
		}
		return false
	})
	if err != nil {
		return Info{}, err
	}
	if actor.Role != authority.Session {
		return m.store().Resolve(ref)
	}
	return info, nil
}
