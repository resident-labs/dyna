package roomsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

type goalAuthorKey struct{}

// WithGoalAuthor carries the author resolved by the engine's recorded-caller policy.
func WithGoalAuthor(ctx context.Context, by string) context.Context {
	return context.WithValue(ctx, goalAuthorKey{}, by)
}

// goalAuthor keeps the recorded author separate from the rule guard and holder lookup.
type goalAuthor struct {
	ID, Name, Address, ResolveAs string
}

// goalCaller resolves the authenticated caller and enforces its room restriction.
func (s *Service) goalCaller(ctx context.Context, ref string) (room.Record, goalAuthor, error) {
	actor := authority.Of(ctx)
	author := goalAuthor{ID: "owner", Name: "owner", Address: "owner", ResolveAs: "owner"}
	callerRoom := ""
	switch actor.Role {
	case authority.Owner, authority.Worker:
	case authority.Root, authority.Session:
		if actor.Session == "" {
			return room.Record{}, goalAuthor{}, fmt.Errorf("goal and rule changes need an authenticated session")
		}
		info, err := s.sessions().Info(actor.Session)
		if err != nil {
			return room.Record{}, goalAuthor{}, err
		}
		author = goalAuthor{ID: info.ID, Name: info.Name, Address: info.Address(), ResolveAs: info.ID}
		callerRoom = info.RoomID
		if ref == "" {
			ref = callerRoom
		}
	default:
		return room.Record{}, goalAuthor{}, fmt.Errorf("goal and rule changes require an owner or session caller")
	}
	if recorded, ok := ctx.Value(goalAuthorKey{}).(string); ok && recorded != "" && recorded != author.ID {
		author.ID, author.Name, author.Address = recorded, recorded, recorded
	} else if actor.Role == authority.Worker {
		return room.Record{}, goalAuthor{}, fmt.Errorf("goal and rule changes need a recorded caller")
	}
	rec, err := s.rooms().Resolve(ref)
	if err == nil && callerRoom != "" && rec.ID != callerRoom {
		err = fmt.Errorf("use the current session's room")
	}
	return rec, author, err
}
