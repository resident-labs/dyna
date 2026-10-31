package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) SessionStop(ctx context.Context, caller contract.Principal, req contract.SessionStopRequest) (contract.SessionStopResponse, *contract.Error) {
	target, err := s.store.Session(ctx, req.Session)
	if err != nil {
		return contract.SessionStopResponse{}, failure(err)
	}
	if e := s.roomAccess(ctx, caller, target.Room); e != nil {
		return contract.SessionStopResponse{}, e
	}
	tree, err := s.store.RoomTree(ctx, target.Room)
	if err != nil {
		return contract.SessionStopResponse{}, failure(err)
	}
	if !tree.CanManage(caller, target.ID) {
		return contract.SessionStopResponse{}, forbidden()
	}
	stopping, err := s.store.RequestTurnStop(ctx, target.ID, s.now())
	if err != nil {
		return contract.SessionStopResponse{}, failure(err)
	}
	return contract.SessionStopResponse{Stopping: stopping}, nil
}

func (s *Service) RoomPause(ctx context.Context, caller contract.Principal, req contract.RoomPauseRequest) (contract.RoomPauseResponse, *contract.Error) {
	if e := s.ruleWriter(ctx, caller, req.Room); e != nil {
		return contract.RoomPauseResponse{}, e
	}
	var body contract.EventBody = contract.RoomResumed{}
	if req.Paused {
		body = contract.RoomPaused{}
	}
	event, err := s.event(req.Room, caller, body)
	if err != nil {
		return contract.RoomPauseResponse{}, failure(err)
	}
	if err := s.store.PauseRoom(ctx, req.Room, req.Paused, event); err != nil {
		return contract.RoomPauseResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.RoomPauseResponse{}, nil
}
