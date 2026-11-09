package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// SessionMove moves a session without changing its box.
func (s *Service) SessionMove(ctx context.Context, caller contract.Principal, req contract.SessionMoveRequest) (contract.SessionMoveResponse, *contract.Error) {
	var response contract.SessionMoveResponse
	session, problem := s.resolveSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	parent, problem := s.resolveSession(ctx, caller, req.NewParent)
	if problem != nil {
		return response, problem
	}
	event, err := s.event(session.Room, caller, contract.SessionMoved{Session: session.ID, NewParent: parent.ID})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.MoveSession(ctx, session.ID, parent.ID, event, s.maxDepth)
	var treeErr *store.TreeError
	if errors.As(err, &treeErr) {
		return response, invalid(treeErr.Error())
	}
	if errors.Is(err, store.ErrTreeForbidden) {
		return response, &contract.Error{Code: contract.ErrForbidden, Message: fmt.Sprintf("you move only sessions below you, to yourself or a session below you; %s or %s is not, so ask your parent", session.Name, parent.Name)}
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}
