package engine

import (
	"context"
	"math"

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
	if e := s.roomAdmin(ctx, caller, req.Room); e != nil {
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

func (s *Service) RoomSetLimits(ctx context.Context, caller contract.Principal, req contract.RoomSetLimitsRequest) (contract.RoomSetLimitsResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomSetLimitsResponse{}, forbidden()
	}
	if req.CPUs < 0 || req.CPUs > math.MaxInt/4 || req.MemBytes < 0 || req.MemBytes > math.MaxInt64/4 {
		return contract.RoomSetLimitsResponse{}, invalid("limits must be non-negative and fit admission accounting")
	}
	event, err := s.event(req.Room, caller, contract.RoomLimitsChanged{CPUs: req.CPUs, MemBytes: req.MemBytes})
	if err != nil {
		return contract.RoomSetLimitsResponse{}, failure(err)
	}
	err = s.store.SetRoomLimits(ctx, req.Room, req.CPUs, req.MemBytes, event)
	if err != nil {
		return contract.RoomSetLimitsResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.RoomSetLimitsResponse{}, nil
}
