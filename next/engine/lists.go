package engine

import (
	"context"
	"database/sql"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) RoomList(ctx context.Context, caller contract.Principal, _ contract.RoomListRequest) (contract.RoomListResponse, *contract.Error) {
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM events").Scan(&seq); err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	rows, err := s.store.Read().QueryContext(ctx, "SELECT r.id,r.name,r.created_at,COALESCE(c.seq,0),r.paused_at IS NOT NULL FROM rooms r LEFT JOIN conversation_reads c ON c.room=r.id AND c.conversation='feed' ORDER BY r.name")
	if err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	defer rows.Close()
	rooms := []contract.RoomSummary{}
	for rows.Next() {
		var room contract.RoomSummary
		var created int64
		if err := rows.Scan(&room.ID, &room.Name, &created, &room.LastRead, &room.Paused); err != nil {
			return contract.RoomListResponse{}, failure(err)
		}
		room.Created = time.UnixMilli(created).UTC()
		room.Waiting = []contract.OpID{}
		room.Working = []contract.SessionID{}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	rows.Close()
	failed, err := s.store.Read().QueryContext(ctx, `SELECT s.room,COUNT(*) FILTER (WHERE (SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND outcome!='stopped' ORDER BY ended_at DESC LIMIT 1)='failed' OR EXISTS (SELECT 1 FROM boxes b WHERE b.session=s.id AND b.state IN ('failed','lost'))),COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM boxes b WHERE b.session=s.id AND b.state='pending' AND b.reason='room-host')) FROM sessions s WHERE s.closed_at IS NULL GROUP BY s.room`)
	if err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	for failed.Next() {
		var id contract.RoomID
		var count, hostless int
		if err := failed.Scan(&id, &count, &hostless); err != nil {
			failed.Close()
			return contract.RoomListResponse{}, failure(err)
		}
		for i := range rooms {
			if rooms[i].ID == id {
				rooms[i].Failed = count
				rooms[i].Hostless = hostless
			}
		}
	}
	err = failed.Err()
	failed.Close()
	if err != nil {
		return contract.RoomListResponse{}, failure(err)
	}
	// newest holds each room's activityOrder of its Latest label.
	newest := make([]uint64, len(rooms))
	for kind, query := range []string{
		"SELECT room,id,'','' FROM operations WHERE state='pending' AND class='owner' ORDER BY created_at",
		"SELECT s.room,s.id,s.name,t.id FROM sessions s JOIN turns t ON t.id=(SELECT id FROM turns WHERE session=s.id AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1) WHERE s.closed_at IS NULL ORDER BY s.created_at,s.id",
	} {
		items, err := s.store.Read().QueryContext(ctx, query)
		if err != nil {
			return contract.RoomListResponse{}, failure(err)
		}
		for items.Next() {
			var room, id, name, turn string
			if err := items.Scan(&room, &id, &name, &turn); err != nil {
				items.Close()
				return contract.RoomListResponse{}, failure(err)
			}
			for i := range rooms {
				if string(rooms[i].ID) == room {
					if kind == 0 {
						rooms[i].Waiting = append(rooms[i].Waiting, contract.OpID(id))
					} else {
						rooms[i].Working = append(rooms[i].Working, contract.SessionID(id))
						s.mu.Lock()
						state := s.sessionLocked(contract.SessionID(id))
						activity, order := state.activity, state.activityOrder
						s.mu.Unlock()
						if activity.Turn == contract.TurnID(turn) && activity.Label != "" && order > newest[i] {
							newest[i] = order
							rooms[i].Latest = &contract.RoomActivity{Session: activity.Session, Name: name, Label: activity.Label}
						}
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
	rows, err := s.store.Read().QueryContext(ctx, `SELECT s.id,s.name,COALESCE(s.parent,''),s.profile,COALESCE(b.id,''),COALESCE(b.state,''),COALESCE(b.reason,''),s.goal,
		COALESCE((SELECT id FROM turns WHERE session=s.id AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1),''),
		COALESCE((SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND outcome!='stopped' ORDER BY ended_at DESC LIMIT 1),''),
		s.shell_jobs, COALESCE(c.seq,0), s.created_at,
		(SELECT ended_at FROM turns WHERE session=s.id AND ended_at IS NOT NULL ORDER BY ended_at DESC LIMIT 1),
		EXISTS (SELECT 1 FROM shell_jobs j WHERE j.session=s.id)
		FROM sessions s LEFT JOIN boxes b ON b.session=s.id LEFT JOIN conversation_reads c ON c.room=s.room AND c.conversation='dm:'||s.id WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id`, req.Room)
	if err != nil {
		return contract.SessionListResponse{}, failure(err)
	}
	defer rows.Close()
	sessions := []contract.SessionSummary{}
	for rows.Next() {
		var session contract.SessionSummary
		var started int64
		var lastTurn sql.NullInt64
		if err := rows.Scan(&session.ID, &session.Name, &session.Parent, &session.Profile, &session.Box, &session.BoxState, &session.BoxReason, &session.Goal, &session.Turn, &session.LastOutcome, &session.ShellJobs, &session.LastRead, &started, &lastTurn, &session.HadJobs); err != nil {
			return contract.SessionListResponse{}, failure(err)
		}
		session.StartedAt = time.UnixMilli(started).UTC()
		if lastTurn.Valid {
			session.LastTurnAt = time.UnixMilli(lastTurn.Int64).UTC()
		}
		s.mu.Lock()
		state := s.sessionLocked(session.ID)
		activity := state.activity
		session.Computer = contract.ComputerStopped
		if state.computer != nil {
			session.Computer = state.computer.state
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
