package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) RoomList(ctx context.Context, caller contract.Principal, _ contract.RoomListRequest) (contract.RoomListResponse, *contract.Error) {
	result, items, err := s.store.RoomList(ctx)
	if err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	newest := make([]uint64, len(result.Rooms))
	for _, item := range items {
		for i := range result.Rooms {
			if result.Rooms[i].ID != item.Room {
				continue
			}
			if item.Operation != "" {
				result.Rooms[i].Waiting = append(result.Rooms[i].Waiting, item.Operation)
				continue
			}
			if item.Question != "" {
				result.Rooms[i].Questions = append(result.Rooms[i].Questions, item.Question)
				continue
			}
			result.Rooms[i].Working = append(result.Rooms[i].Working, item.Session)
			activity, order := s.live.Activity(item.Session)
			if activity.Turn == item.Turn && activity.Label != "" && order > newest[i] {
				newest[i] = order
				result.Rooms[i].Latest = &contract.RoomActivity{Session: activity.Session, Name: item.Name, Label: activity.Label}
			}
		}
	}
	return result, nil
}

func (s *Service) SessionList(ctx context.Context, caller contract.Principal, req contract.SessionListRequest) (contract.SessionListResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.SessionListResponse{}, e
	}
	// A session reads the list as tool output, which its harness cuts at about 10 KB, so it gets a page unless it asks for a size.
	limit := req.Limit
	if limit == 0 && caller.Kind == contract.PrincipalSession {
		limit = contract.SessionListPage
	}
	result, err := s.store.SessionList(ctx, req, limit)
	if err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	for i := range result.Sessions {
		session := &result.Sessions[i]
		if s.startStatus != nil && (session.BoxState == contract.BoxCreating || session.BoxState == contract.BoxStarting) {
			session.Start = s.startStatus(session.Box)
		}
		activity, _ := s.live.Activity(session.ID)
		session.Computer = contract.ComputerStopped
		if handle, ok := s.live.LookupSession(session.ID); ok && handle.Lock() {
			session.Computer = handle.ComputerState()
			handle.Unlock()
		}
		if session.Turn != "" && activity.Turn == session.Turn {
			session.Activity = activity.Label
			session.ActivityKind = activity.Kind
		}
	}
	return result, nil
}
