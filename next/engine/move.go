package engine

import (
	"context"
	"slices"
	"sort"

	"github.com/resident-labs/dyna/contract"
)

type moveBoxes interface {
	RemoveContainer(context.Context, contract.BoxID) error
	Recreate(context.Context, BoxSpec, string) error
}

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
	if !tree.CanManage(caller, req.Session) || !tree.CanManage(caller, req.NewParent) {
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
	s.admit.Lock()
	defer s.admit.Unlock()
	tree, err = s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return response, failure(err)
	}
	current, err := tree.CloseOrder(req.Session)
	if err != nil || !slices.Equal(current, order) {
		return response, &contract.Error{Code: contract.ErrConflict, Message: "subtree changed; retry the move"}
	}
	if !tree.CanManage(caller, req.Session) || !tree.CanManage(caller, req.NewParent) {
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
	host, ok := s.boxes.(moveBoxes)
	if !ok {
		return response, &contract.Error{Code: contract.ErrUnavailable, Message: "box host cannot move containers"}
	}
	busy, err := s.store.MoveSession(ctx, req.Session, req.NewParent, budgetParent, event)
	if err != nil {
		return response, failure(err)
	}
	if busy != "" {
		return response, &contract.Error{Code: contract.ErrMoveBusy, Message: "session " + string(busy) + " is busy"}
	}
	for _, id := range order {
		member, err := s.store.Session(ctx, id)
		if err != nil {
			return response, failure(err)
		}
		if err := s.boxes.Stop(context.WithoutCancel(ctx), member.Box); err != nil {
			return response, failure(err)
		}
		if err := host.RemoveContainer(context.WithoutCancel(ctx), member.Box); err != nil {
			return response, failure(err)
		}
	}
	return response, nil
}
