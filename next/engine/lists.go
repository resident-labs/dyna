package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) RoomList(ctx context.Context, caller contract.Principal, _ contract.RoomListRequest) (contract.RoomListResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomListResponse{}, forbidden()
	}
	rows, err := s.store.Read().QueryContext(ctx, "SELECT id,name FROM rooms ORDER BY name")
	if err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	defer rows.Close()
	rooms := []contract.RoomSummary{}
	for rows.Next() {
		var room contract.RoomSummary
		if err := rows.Scan(&room.ID, &room.Name); err != nil {
			return contract.RoomListResponse{}, failure(err)
		}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	return contract.RoomListResponse{Rooms: rooms}, nil
}

func (s *Service) SessionList(ctx context.Context, caller contract.Principal, req contract.SessionListRequest) (contract.SessionListResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.SessionListResponse{}, e
	}
	// The seq is read first, so events after it may repeat what the list shows but never miss a change.
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events WHERE room=?", req.Room).Scan(&seq); err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	rows, err := s.store.Read().QueryContext(ctx, `SELECT s.id,s.name,COALESCE(s.parent,''),s.profile,COALESCE(b.id,''),COALESCE(b.state,''),s.goal,
		COALESCE((SELECT id FROM turns WHERE session=s.id AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1),''),
		COALESCE((SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND outcome!='stopped' ORDER BY ended_at DESC LIMIT 1),''),
		MAX(0,COALESCE((SELECT shell_jobs FROM turns WHERE session=s.id AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 1),0))
		FROM sessions s LEFT JOIN boxes b ON b.session=s.id WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id`, req.Room)
	if err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	defer rows.Close()
	sessions := []contract.SessionSummary{}
	for rows.Next() {
		var session contract.SessionSummary
		if err := rows.Scan(&session.ID, &session.Name, &session.Parent, &session.Profile, &session.Box, &session.BoxState, &session.Goal, &session.Turn, &session.LastOutcome, &session.ShellJobs); err != nil {
			return contract.SessionListResponse{}, failure(err)
		}
		s.mu.Lock()
		activity := s.activity[session.ID]
		session.Computer = contract.ComputerStopped
		if c := s.computers[session.ID]; c != nil {
			session.Computer = c.state
		}
		s.mu.Unlock()
		if session.Turn != "" && activity.Turn == session.Turn {
			session.Activity = activity.Label
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	return contract.SessionListResponse{Sessions: sessions, Seq: seq}, nil
}
