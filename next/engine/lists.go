package engine

import (
	"context"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) RoomList(ctx context.Context, caller contract.Principal, _ contract.RoomListRequest) (contract.RoomListResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomListResponse{}, forbidden()
	}
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events").Scan(&seq); err != nil {
		return contract.RoomListResponse{}, failure(err)
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
		room.Waiting = []contract.OpID{}
		room.Working = []contract.SessionID{}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	rows.Close()
	for kind, query := range []string{
		"SELECT room,id FROM operations WHERE state='pending' AND class='owner' ORDER BY created_at",
		"SELECT s.room,s.id FROM sessions s WHERE s.closed_at IS NULL AND EXISTS (SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL) ORDER BY s.created_at,s.id",
	} {
		items, err := s.store.Read().QueryContext(ctx, query)
		if err != nil {
			return contract.RoomListResponse{}, failure(err)
		}
		for items.Next() {
			var room, id string
			if err := items.Scan(&room, &id); err != nil {
				items.Close()
				return contract.RoomListResponse{}, failure(err)
			}
			for i := range rooms {
				if string(rooms[i].ID) == room {
					if kind == 0 {
						rooms[i].Waiting = append(rooms[i].Waiting, contract.OpID(id))
					} else {
						rooms[i].Working = append(rooms[i].Working, contract.SessionID(id))
					}
				}
			}
		}
		err = items.Err()
		items.Close()
		if err != nil {
			return contract.RoomListResponse{}, failure(err)
		}
	}
	return contract.RoomListResponse{Rooms: rooms, Seq: seq}, nil
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
		COALESCE((SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 1),'')
		FROM sessions s LEFT JOIN boxes b ON b.session=s.id WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id`, req.Room)
	if err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	defer rows.Close()
	sessions := []contract.SessionSummary{}
	for rows.Next() {
		var session contract.SessionSummary
		if err := rows.Scan(&session.ID, &session.Name, &session.Parent, &session.Profile, &session.Box, &session.BoxState, &session.Goal, &session.Turn, &session.LastOutcome); err != nil {
			return contract.SessionListResponse{}, failure(err)
		}
		s.mu.Lock()
		session.Computer = contract.ComputerStopped
		if c := s.computers[session.ID]; c != nil {
			session.Computer = c.state
		}
		s.mu.Unlock()
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	return contract.SessionListResponse{Sessions: sessions, Seq: seq}, nil
}
