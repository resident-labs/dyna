package engine

import (
	"context"
	"github.com/resident-labs/dyna/contract"
)

// inputInterrupt checks actual event identity once, where the engine owns the
// tree. A forwarded post's outside author is not a local ancestor.
func (s *Service) inputInterrupt(ctx context.Context, viewer contract.SessionID, event contract.Event, surface contract.Surface) (*contract.Interrupt, error) {
	if surface == contract.SurfaceEngine {
		return nil, nil
	}
	if post, ok := event.Body.(contract.PostCreated); ok && (post.Source != nil || post.Hook != "") {
		return nil, nil
	}
	switch event.From.Kind {
	case contract.PrincipalOwner:
		return &contract.Interrupt{Kind: contract.InterruptOwner}, nil
	case contract.PrincipalSession:
		tree, err := s.store.RoomTree(ctx, event.Room)
		if err != nil {
			return nil, err
		}
		if !tree.Above(event.From.Session, viewer) {
			return nil, nil
		}
		name, _, _, err := s.store.FrameSession(ctx, viewer, event.From.Session)
		if err != nil {
			return nil, err
		}
		kind := contract.InterruptAncestor
		if tree.Parent(viewer) == event.From.Session {
			kind = contract.InterruptParent
		}
		return &contract.Interrupt{Kind: kind, Name: name}, nil
	}
	return nil, nil
}
