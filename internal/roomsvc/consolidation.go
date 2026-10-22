package roomsvc

import (
	"context"
	"fmt"

	"dyna-agent/internal/subagent"
)

func (s *Service) ConsolidateSessions(ctx context.Context, roomRef string, opts subagent.ConsolidationOptions) (subagent.ConsolidationState, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return subagent.ConsolidationState{}, err
	}
	// Verify every reference, and that the caller may act on it, before the
	// manager reserves anything.
	for _, ref := range append(append([]string(nil), opts.Sessions...), opts.Target) {
		if _, err = s.actOnSession(ctx, rec.ID, ref); err != nil {
			return subagent.ConsolidationState{}, err
		}
	}
	state, err := s.Notify.Sessions.Consolidate(ctx, opts)
	return state.Summary(), err
}

func (s *Service) SessionConsolidations(roomRef string) ([]subagent.ConsolidationState, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	states, err := s.Notify.Sessions.SessionConsolidations(rec.ID)
	for i := range states {
		states[i] = states[i].Summary()
	}
	return states, err
}

func (s *Service) ResumeSessionConsolidation(ctx context.Context, roomRef, id string) (subagent.ConsolidationState, error) {
	rec, err := s.actOnConsolidation(ctx, roomRef, id)
	if err != nil {
		return subagent.ConsolidationState{}, err
	}
	state, err := s.Notify.Sessions.ResumeConsolidation(ctx, rec, id)
	return state.Summary(), err
}

func (s *Service) CancelSessionConsolidation(ctx context.Context, roomRef, id string) (subagent.ConsolidationState, error) {
	rec, err := s.actOnConsolidation(ctx, roomRef, id)
	if err != nil {
		return subagent.ConsolidationState{}, err
	}
	state, err := s.Notify.Sessions.CancelConsolidation(ctx, rec, id)
	return state.Summary(), err
}

// actOnConsolidation resolves the room of consolidation id and checks that
// the caller may act on each of its sessions, because resume and cancel
// change every one of them. It returns the room ID.
func (s *Service) actOnConsolidation(ctx context.Context, roomRef, id string) (string, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return "", err
	}
	states, err := s.Notify.Sessions.SessionConsolidations(rec.ID)
	if err != nil {
		return "", err
	}
	for _, state := range states {
		if state.ID != id {
			continue
		}
		for _, ref := range state.Sessions {
			if _, err := s.actOnSession(ctx, rec.ID, ref); err != nil {
				return "", err
			}
		}
		return rec.ID, nil
	}
	return "", fmt.Errorf("no such consolidation in room %s", rec.Name)
}
