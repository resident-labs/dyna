package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

// markSilent notes a turn that ended with an open goal and no reply, so its parent hears of it without the idle wait.
func markSilent(ctx context.Context, tx *sql.Tx, session contract.SessionID, turn contract.TurnID) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO checkins(session,silent_turn) SELECT ?1,?2 WHERE EXISTS (SELECT 1 FROM goals WHERE session=?1 AND state='active')
		ON CONFLICT(session) DO UPDATE SET silent_turn=excluded.silent_turn`, session, turn)
	return err
}

// markFailureTold records that a failed turn's own reply already told the parent, unless the parent cannot read it.
func markFailureTold(ctx context.Context, tx *sql.Tx, session contract.SessionID) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO checkins(session,failure) SELECT ?1,'turn' WHERE EXISTS (SELECT 1 FROM sessions s JOIN boxes b ON b.session=s.parent WHERE s.id=?1 AND b.state NOT IN ('failed','lost'))
		ON CONFLICT(session) DO UPDATE SET failure='turn'`, session)
	return err
}

// failureKey is the failure a session is in now: room.FailureBox, room.FailureTurn or nothing. A stopped turn neither fails nor recovers.
const failureKey = `CASE WHEN b.state IN ('failed','lost') THEN 'box' WHEN (SELECT outcome FROM turns WHERE session=s.id AND ended_at IS NOT NULL AND outcome!='stopped' ORDER BY ended_at DESC LIMIT 1)='failed' THEN 'turn' ELSE '' END`

// failureRooms lists the rooms with a failure nobody has heard of, or a recovery not yet recorded.
func failureRooms(ctx context.Context, q sqlQuery) ([]contract.RoomID, error) {
	return listIDs[contract.RoomID](ctx, q, `SELECT DISTINCT s.room FROM sessions s JOIN rooms r ON r.id=s.room JOIN boxes b ON b.session=s.id LEFT JOIN checkins c ON c.session=s.id
		WHERE s.closed_at IS NULL AND s.state='open' AND s.parent IS NOT NULL AND r.paused_at IS NULL AND r.state='open' AND `+failureKey+`!=COALESCE(c.failure,'') ORDER BY s.room`)
}

// checkinRooms lists the rooms a pass looks at: open, not paused, with a held goal.
func checkinRooms(ctx context.Context, q sqlQuery) ([]contract.RoomID, error) {
	return listIDs[contract.RoomID](ctx, q, `SELECT DISTINCT r.id FROM goals g JOIN rooms r ON r.id=g.room
		WHERE g.state='active' AND g.session IS NOT NULL AND r.paused_at IS NULL AND r.state='open' ORDER BY r.id`)
}

const checkinFacts = `SELECT s.id,COALESCE(s.parent,''),s.name,s.state='closing',s.created_at,s.shell_jobs,s.model_retry_at>0,b.state,b.updated_at,
	COALESCE(t.id,''),COALESCE(t.started_at,0),COALESCE(t.ended_at,0),COALESCE(t.outcome,''),
	EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.wake=1 AND d.state='pending')
		OR EXISTS (SELECT 1 FROM operations o WHERE o.by_kind='session' AND o.by_id=s.id AND o.state IN ('approved','running')),
	COALESCE((SELECT MIN(o.created_at) FROM operations o WHERE o.by_kind='session' AND o.by_id=s.id AND o.state='pending'),0),
	COALESCE(c.silent_turn,''),COALESCE(c.progress,0),COALESCE(c.told,0),COALESCE(c.told_at,0),COALESCE(c.reported_at,0),
	EXISTS (SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.seq=c.report_seq AND d.state IN ('pending','taken')),
	` + failureKey + `,COALESCE(c.failure,''),b.reason,NOT ` + noOpenQuestion + `,` + blockedOnQuestion + `
	FROM sessions s JOIN boxes b ON b.session=s.id
	LEFT JOIN turns t ON t.id=(SELECT id FROM turns WHERE session=s.id ORDER BY started_at DESC, rowid DESC LIMIT 1)
	LEFT JOIN checkins c ON c.session=s.id
	WHERE s.room=? AND s.closed_at IS NULL ORDER BY s.created_at,s.id`

const checkinGoals = `SELECT g.session,g.number,g.text,g.updated_at,(SELECT COUNT(*) FROM goal_items i WHERE i.room=g.room AND i.goal=g.number AND i.state='open')
	FROM goals g WHERE g.room=? AND g.state='active' AND g.session IS NOT NULL ORDER BY g.number`

// readCheckinRoom loads the planner's facts about one room. A goal that moved since the stall was told starts the stall over.
func readCheckinRoom(ctx context.Context, tx *sql.Tx, id contract.RoomID) ([]room.CheckinSession, map[contract.SessionID]int64, error) {
	rows, err := tx.QueryContext(ctx, checkinFacts, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	millis := func(value int64) time.Time {
		if value == 0 {
			return time.Time{}
		}
		return time.UnixMilli(value)
	}
	var sessions []room.CheckinSession
	stalls := make(map[contract.SessionID]int64)
	for rows.Next() {
		var s room.CheckinSession
		var created, boxChanged, started, ended, waiting, progress, told, reported int64
		var turn, silent, outcome string
		if err := rows.Scan(&s.ID, &s.Parent, &s.Name, &s.Closing, &created, &s.Jobs, &s.Retrying, &s.Box, &boxChanged,
			&turn, &started, &ended, &outcome, &s.Pending, &waiting, &silent, &progress, &s.Told, &told, &reported, &s.Unread, &s.Failure, &s.FailureTold, &s.Reason, &s.Asking, &s.Blocked); err != nil {
			return nil, nil, err
		}
		s.BoxChanged, s.Waiting, s.ToldAt, s.Reported = millis(boxChanged), millis(waiting), millis(told), millis(reported)
		s.Turn = turn != "" && ended == 0
		s.Failed = outcome == string(contract.TurnFailed)
		s.Silent = turn != "" && turn == silent
		switch {
		case s.Turn:
			s.Last = millis(started)
		case turn != "":
			s.Last = millis(ended)
		default:
			s.Last = millis(created)
		}
		stalls[s.ID] = progress
		sessions = append(sessions, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	bySession := make(map[contract.SessionID]*room.CheckinSession, len(sessions))
	for i := range sessions {
		bySession[sessions[i].ID] = &sessions[i]
	}
	goals, err := tx.QueryContext(ctx, checkinGoals, id)
	if err != nil {
		return nil, nil, err
	}
	defer goals.Close()
	for goals.Next() {
		var holder contract.SessionID
		var number, updated int64
		var text string
		var open int
		if err := goals.Scan(&holder, &number, &text, &updated, &open); err != nil {
			return nil, nil, err
		}
		s := bySession[holder]
		if s == nil {
			continue
		}
		if s.Goal == "" {
			s.Goal, s.Text, s.Open = goalID(number), text, open
		}
		if at := time.UnixMilli(updated); at.After(s.Progress) {
			s.Progress = at
		}
	}
	if err := goals.Err(); err != nil {
		return nil, nil, err
	}
	for i := range sessions {
		s := &sessions[i]
		var current int64
		if !s.Progress.IsZero() {
			current = s.Progress.UnixMilli()
		}
		if current != stalls[s.ID] {
			s.Told, s.ToldAt = 0, time.Time{}
		}
		stalls[s.ID] = current
	}
	return sessions, stalls, nil
}

// report writes one engine notice and its waking delivery, and records it against the reader, so a report nobody reads shows.
func (s *Store) report(ctx context.Context, tx *sql.Tx, id contract.RoomID, to contract.SessionID, at time.Time, body contract.EventBody, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) error {
	event, err := newEvent(id, body)
	if err != nil {
		return err
	}
	seq, err := s.AppendEvent(tx, event)
	if err != nil {
		return err
	}
	reader := contract.Principal{Kind: contract.PrincipalSession, Session: to}
	if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: reader, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO checkins(session,reported_at,report_seq) VALUES(?1,?2,?3) ON CONFLICT(session) DO UPDATE SET reported_at=?2,report_seq=?3", to, at.UnixMilli(), seq)
	return err
}

// markTold records how far up a stall has gone, against the goal state it was told from.
func markTold(ctx context.Context, tx *sql.Tx, child contract.SessionID, progress int64, told int, at time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO checkins(session,progress,told,told_at) VALUES(?1,?2,?3,?4) ON CONFLICT(session) DO UPDATE SET progress=?2,told=?3,told_at=?4", child, progress, told, at.UnixMilli())
	return err
}

// turnFailure reads why a session's last turn failed.
func turnFailure(ctx context.Context, tx *sql.Tx, id contract.RoomID, session contract.SessionID) (contract.FailureCause, string, error) {
	var data []byte
	err := tx.QueryRowContext(ctx, "SELECT body FROM events WHERE room=? AND from_kind=? AND from_id=? AND type='turn.ended' ORDER BY seq DESC LIMIT 1", id, contract.PrincipalSession, session).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	var ended contract.TurnEnded
	if err := json.Unmarshal(data, &ended); err != nil {
		return "", "", err
	}
	return ended.Cause, ended.Error, nil
}

// SendFailureNotices tells ancestors of new failures and records recoveries. It writes only when something changed, so
// it can run on every engine tick. Clocks and records live in the store, so a restart repeats nothing and misses nothing.
func (s *Store) SendFailureNotices(ctx context.Context, now time.Time, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) error {
	rooms, err := failureRooms(ctx, s.Read())
	if err != nil || len(rooms) == 0 {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		rooms, err := failureRooms(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range rooms {
			sessions, progress, err := readCheckinRoom(ctx, tx, id)
			if err != nil {
				return err
			}
			plans, settled := room.PlanFailures(sessions, now)
			for _, plan := range plans {
				for i := range plan.Failures {
					failure := &plan.Failures[i]
					if failure.Box != "" {
						continue
					}
					if failure.Cause, failure.Reason, err = turnFailure(ctx, tx, id, failure.Session); err != nil {
						return err
					}
				}
				if err := s.report(ctx, tx, id, plan.To, now, contract.SessionFailed{To: plan.To, Failures: plan.Failures}, newEvent); err != nil {
					return err
				}
				for child, told := range plan.Told {
					if err := markTold(ctx, tx, child, progress[child], told, now); err != nil {
						return err
					}
				}
			}
			for session, failure := range settled {
				if _, err := tx.ExecContext(ctx, "INSERT INTO checkins(session,failure) VALUES(?1,?2) ON CONFLICT(session) DO UPDATE SET failure=?2", session, failure); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// SendCheckins runs one check-in pass. The read, the plan and the writes share one transaction, so a crash leaves
// either the whole report or none of it, and a restart picks up the same clocks.
func (s *Store) SendCheckins(ctx context.Context, now time.Time, newEvent func(contract.RoomID, contract.EventBody) (contract.Event, error)) error {
	rooms, err := checkinRooms(ctx, s.Read())
	if err != nil || len(rooms) == 0 {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		rooms, err := checkinRooms(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range rooms {
			sessions, progress, err := readCheckinRoom(ctx, tx, id)
			if err != nil {
				return err
			}
			for _, plan := range room.PlanCheckins(sessions, now) {
				if err := s.report(ctx, tx, id, plan.To, now, contract.CheckinDue{Session: plan.To, Children: plan.Children}, newEvent); err != nil {
					return err
				}
				for child, told := range plan.Told {
					if err := markTold(ctx, tx, child, progress[child], told, now); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}
