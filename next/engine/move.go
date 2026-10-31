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
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return response, failure(err)
	}
	if !tree.CanManage(caller, req.Session) || !(tree.CanManage(caller, req.NewParent) || caller.Kind == contract.PrincipalSession && caller.Session == req.NewParent) {
		return response, forbidden()
	}
	if err := tree.CheckMove(req.Session, req.NewParent); err != nil {
		return response, invalid(err.Error())
	}
	event, err := s.event(session.Room, caller, contract.SessionMoved{Session: req.Session, NewParent: req.NewParent})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.MoveSession(ctx, req.Session, req.NewParent, event)
	if errors.Is(err, store.ErrMoveCycle) {
		return response, invalid(err.Error())
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}
