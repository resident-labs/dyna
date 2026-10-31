package engine

import (
	"context"
	"errors"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// SessionMove moves a session without changing its box.
func (s *Service) SessionMove(ctx context.Context, caller contract.Principal, req contract.SessionMoveRequest) (contract.SessionMoveResponse, *contract.Error) {
	var response contract.SessionMoveResponse
	session, err := s.store.Session(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	event, err := s.event(session.Room, caller, contract.SessionMoved{Session: req.Session, NewParent: req.NewParent})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.MoveSession(ctx, req.Session, req.NewParent, event, s.maxDepth)
	var treeErr *store.TreeError
	if errors.As(err, &treeErr) {
		return response, invalid(treeErr.Error())
	}
	if errors.Is(err, store.ErrTreeForbidden) {
		return response, forbidden()
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}
