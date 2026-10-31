package engine

import (
	"context"
	"slices"
	"sort"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// SessionMove moves an idle subtree and leaves its volumes for the next wake.
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
	order, err := tree.CloseOrder(req.Session)
	if err != nil {
		return response, invalid(err.Error())
	}
	ids := slices.Clone(order)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var unlocks []func()
	for _, id := range ids {
		unlocks = append(unlocks, s.lockBox(id))
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	tree, err = s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return response, failure(err)
	}
	current, err := tree.CloseOrder(req.Session)
	if err != nil || !slices.Equal(current, order) {
		return response, &contract.Error{Code: contract.ErrConflict, Message: "subtree changed; retry the move"}
	}
	if !tree.CanManage(caller, req.Session) || !(tree.CanManage(caller, req.NewParent) || caller.Kind == contract.PrincipalSession && caller.Session == req.NewParent) {
		return response, forbidden()
	}
	if err := tree.CheckMove(req.Session, req.NewParent); err != nil {
		return response, invalid(err.Error())
	}
	parent, err := s.store.Session(ctx, req.NewParent)
	if err != nil {
		return response, failure(err)
	}
	var budgetParent contract.BoxID
	if req.NewParent != tree.Root() {
		budgetParent = parent.Box
	}
	event, err := s.event(session.Room, caller, contract.SessionMoved{Session: req.Session, NewParent: req.NewParent})
	if err != nil {
		return response, failure(err)
	}
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return response, failure(err)
	}
	var members []box.Box
	var boxEvents []contract.Event
	for _, id := range order {
		for _, b := range boxes {
			if b.Session == id {
				members = append(members, b)
				e, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
				if err != nil {
					return response, failure(err)
				}
				boxEvents = append(boxEvents, e)
				break
			}
		}
	}
	busy, uncreated, err := s.store.MoveSession(ctx, req.Session, req.NewParent, budgetParent, event, members, boxEvents)
	if err != nil {
		return response, failure(err)
	}
	if busy != "" {
		reason := " is busy"
		if uncreated {
			reason = " has a box that is not created yet"
		}
		return response, &contract.Error{Code: contract.ErrMoveBusy, Message: "session " + string(busy) + reason}
	}
	return response, nil
}
