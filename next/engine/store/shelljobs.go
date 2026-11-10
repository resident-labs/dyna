package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// ShellJobEnded takes the job off what the session holds, and appends the event and a waking delivery to the session itself
// unless the end is silent. A job that shell_jobs already holds is skipped, so a retry adds no second wake.
func (s *Store) ShellJobEnded(ctx context.Context, session contract.SessionID, job contract.ShellJobID, event contract.Event, silent bool) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO shell_jobs(session,job) VALUES(?,?)", session, job)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			return err
		}
		if err := dropJobRunning(ctx, tx, session, job); err != nil {
			return err
		}
		// While a turn is open its end replaces the count, so only an idle session lowers it here.
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET shell_jobs=MAX(0,shell_jobs-1) WHERE id=? AND NOT EXISTS (SELECT 1 FROM turns WHERE session=? AND ended_at IS NULL)", session, session); err != nil {
			return err
		}
		if silent {
			return nil
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		reply := contract.ReplyChannel{Kind: contract.ReplyNone}
		// An open turn has not published its new count yet. Use the last named
		// snapshot, excluding ends already received, rather than its stale count.
		var remaining int
		err = tx.QueryRowContext(ctx, `SELECT CASE WHEN json_array_length(json_extract(CAST(e.body AS TEXT),'$.jobs'))>0
			THEN (SELECT COUNT(*) FROM json_each(json_extract(CAST(e.body AS TEXT),'$.jobs')) j WHERE NOT EXISTS (SELECT 1 FROM shell_jobs x WHERE x.session=s.id AND x.job=j.value))
			ELSE s.shell_jobs END
			FROM sessions s LEFT JOIN events e ON e.seq=(SELECT MAX(seq) FROM events WHERE room=? AND from_kind='session' AND from_id=s.id AND type='turn.ended') WHERE s.id=?`, event.Room, session).Scan(&remaining)
		if err != nil {
			return err
		}
		if remaining == 0 {
			var turn contract.TurnID
			err = tx.QueryRowContext(ctx, `SELECT json_extract(CAST(e.body AS TEXT),'$.turn') FROM events e
				WHERE e.room=? AND e.from_kind='session' AND e.from_id=? AND e.type='turn.ended'
				AND EXISTS (SELECT 1 FROM json_each(json_extract(CAST(e.body AS TEXT),'$.jobs')) WHERE value=?) ORDER BY e.seq LIMIT 1`, event.Room, session, job).Scan(&turn)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil {
				var home string
				err = tx.QueryRowContext(ctx, `SELECT reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to FROM deliveries WHERE to_kind='session' AND to_id=? AND turn=? AND reply_kind='final' ORDER BY seq DESC LIMIT 1`, session, turn).Scan(&reply.Kind, &reply.Target.Kind, &reply.Target.Session, &home, &reply.ReplyTo)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
				if err == nil {
					kind, id, _ := strings.Cut(home, ":")
					reply.Home = contract.Conversation{Kind: contract.ConversationKind(kind), Session: contract.SessionID(id)}
				}
			}
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: event.From, Surface: contract.SurfaceEngine, Reply: reply, Wake: true})
	})
}

// jobRunningNotice finds a job's running status among deliveries. It is not work: it holds no close and does not pass to the
// parent, whose job it is not.
const jobRunningNotice = `EXISTS (SELECT 1 FROM events e WHERE e.seq=d.seq AND e.type='shell.job.running')`

// ShellJobRunning delivers a job's status. An idle session wakes for it, and a session with a turn open reads it with its next
// turn. The check is in this write, so a turn cannot open between it and the delivery. A job that has ended sends nothing.
func (s *Store) ShellJobRunning(ctx context.Context, session contract.SessionID, job contract.ShellJobID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var ended, inTurn bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM shell_jobs WHERE session=?1 AND job=?2),EXISTS (SELECT 1 FROM turns WHERE session=?1 AND ended_at IS NULL)`, session, job).Scan(&ended, &inTurn)
		if err != nil || ended {
			return err
		}
		if err := dropJobRunning(ctx, tx, session, job); err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		return s.InsertDelivery(ctx, tx, seq, event.Body, contract.Delivery{To: event.From, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: !inTurn})
	})
}

// dropJobRunning takes back a status nobody has read. A newer one replaces it, and once the job ends it is no longer true.
func dropJobRunning(ctx context.Context, tx *sql.Tx, session contract.SessionID, job contract.ShellJobID) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM deliveries AS d WHERE d.to_kind='session' AND d.to_id=? AND d.state='pending' AND EXISTS (SELECT 1 FROM events e WHERE e.seq=d.seq AND e.type='shell.job.running' AND json_extract(CAST(e.body AS TEXT),'$.job')=?)`, session, job)
	return err
}

// RepliesOwed lists the replies a session still owes after turns that ended while it waited for a job or a child.
func (s *Store) RepliesOwed(ctx context.Context, session contract.SessionID) ([]contract.ReplyChannel, error) {
	var kept []byte
	err := s.Read().QueryRowContext(ctx, "SELECT replies FROM replies_owed WHERE session=?", session).Scan(&kept)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var owed []contract.ReplyChannel
	return owed, json.Unmarshal(kept, &owed)
}

// WakeOwedReplies wakes each session that owes a reply and has nothing left to start it: what it waited for went without
// an input, as a job does when its box is replaced and a child does when it is closed. The turn the notice starts pays the reply.
// A session with a model retry pending or cancelled is left alone: the retry runs its own turn, and a stop must stay a stop.
func (s *Store) WakeOwedReplies(ctx context.Context, now time.Time) error {
	const stranded = `SELECT s.id,s.room FROM replies_owed o JOIN sessions s ON s.id=o.session WHERE s.closed_at IS NULL AND s.shell_jobs=0 AND s.model_retry_at=0
		AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.session=s.id AND t.ended_at IS NULL)
		AND NOT EXISTS(SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=s.id AND d.state='pending' AND d.wake=1)
		AND NOT EXISTS(SELECT 1 FROM sessions c WHERE c.parent=s.id AND c.closed_at IS NULL AND (
			EXISTS(SELECT 1 FROM turns t WHERE t.session=c.id AND t.ended_at IS NULL)
			OR EXISTS(SELECT 1 FROM deliveries d WHERE d.to_kind='session' AND d.to_id=c.id AND d.state='pending' AND d.wake=1)))`
	var any bool
	if err := s.Read().QueryRowContext(ctx, "SELECT EXISTS("+stranded+")").Scan(&any); err != nil || !any {
		return err
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, stranded)
		if err != nil {
			return err
		}
		var sessions []Session
		for rows.Next() {
			var session Session
			if err := rows.Scan(&session.ID, &session.Room); err != nil {
				rows.Close()
				return err
			}
			sessions = append(sessions, session)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, session := range sessions {
			body := contract.SessionNotice{Session: session.ID, Text: "What you were waiting for is gone without a result: a job lost with its box, or a child that was closed."}
			seq, err := s.appendEngineEvent(tx, session.Room, now, body)
			if err != nil {
				return err
			}
			if err := s.InsertDelivery(ctx, tx, seq, body, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: session.ID}, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}); err != nil {
				return err
			}
		}
		return nil
	})
}
