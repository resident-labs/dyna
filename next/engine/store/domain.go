package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/room"
)

var ErrParentClosing = errors.New("parent session is closing")

// Session describes the stored session and its box.
type Session struct {
	ID          contract.SessionID
	Room        contract.RoomID
	Name        string
	Repo        contract.RepoName
	Profile     contract.ProfileName
	Box         contract.BoxID
	NewRoomName string // nonempty only for a new room root
}

func (s *Store) CreateRoom(ctx context.Context, id contract.RoomID, name string, at time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES(?,?,?,?,?)", id, name, at.UnixMilli(), 0, 0)
		return err
	})
}

func (s *Store) StartSession(ctx context.Context, session Session, parent contract.SessionID, budgetParent contract.BoxID, goal string, event, boxEvent contract.Event, reason string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if parent != "" {
			var state string
			if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=? AND room=?", parent, session.Room).Scan(&state); err != nil {
				return err
			}
			if state == "closing" || state == "closed" {
				return ErrParentClosing
			}
		}
		if session.NewRoomName != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES(?,?,?,?,?)", session.Room, session.NewRoomName, event.Time.UnixMilli(), 0, 0); err != nil {
				return err
			}
		}
		var parentID *contract.SessionID
		if parent != "" {
			parentID = &parent
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,room,name,parent,repo,profile,state,goal,created_at) VALUES(?,?,?,?,?,?,?,?,?)", session.ID, session.Room, session.Name, parentID, session.Repo, session.Profile, "pending", goal, event.Time.UnixMilli())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO boxes(id,session,parent,image,size,state,reason,slice,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)", session.Box, session.ID, nullableBox(budgetParent), "", contract.BoxSizeSmall, contract.BoxPending, reason, "", event.Time.UnixMilli(), event.Time.UnixMilli())
		if err != nil {
			return err
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		reply := contract.ReplyChannel{Kind: contract.ReplyNone}
		if parent != "" {
			reply = contract.ReplyChannel{Kind: contract.ReplyFinal, Target: contract.Principal{Kind: contract.PrincipalSession, Session: parent}, Home: contract.Conversation{Kind: contract.ConversationFeed}}
		}
		err = insertDelivery(ctx, tx, seq, contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: session.ID}, Surface: contract.SurfaceSession, Reply: reply, Wake: true})
		if err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, boxEvent)
		return err
	})
}

func nullableBox(id contract.BoxID) *contract.BoxID {
	if id == "" {
		return nil
	}
	return &id
}

func (s *Store) CloseSession(ctx context.Context, id contract.SessionID, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE sessions SET state='closed',closed_at=? WHERE id=? AND closed_at IS NULL", event.Time.UnixMilli(), id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pr_owners SET session=(SELECT parent FROM sessions WHERE id=?) WHERE session=? AND EXISTS (SELECT 1 FROM sessions WHERE id=? AND parent IS NOT NULL)", id, id, id); err != nil {
			return err
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
}

func (s *Store) DeliveryDone(ctx context.Context, session contract.SessionID, seq contract.DeliverySeq) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_kind=? AND to_id=? AND seq=? AND state IN ('taken','read')", contract.PrincipalSession, session, seq)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return sql.ErrNoRows
		}
		return nil
	})
}

var ErrTurnConflict = errors.New("turn output differs from the committed result")

func (s *Store) EndTurn(ctx context.Context, session contract.SessionID, output contract.TurnOutputRequest, event contract.Event) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE turns SET ended_at=?,outcome=?,model=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=? WHERE id=? AND session=? AND ended_at IS NULL", event.Time.UnixMilli(), output.Outcome, output.Model, output.Usage.InputTokens, output.Usage.OutputTokens, output.Usage.CacheReadTokens, output.Usage.CacheWriteTokens, output.Turn, session)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			var body []byte
			err := tx.QueryRowContext(ctx, "SELECT e.body FROM events e JOIN sessions s ON e.room=s.room WHERE s.id=? AND e.type='turn.ended' AND json_extract(e.body,'$.turn')=?", session, output.Turn).Scan(&body)
			if err != nil {
				return err
			}
			var previous contract.TurnEnded
			if err := json.Unmarshal(body, &previous); err != nil {
				return err
			}
			current := event.Body.(contract.TurnEnded)
			if previous.Session != current.Session || previous.Turn != current.Turn || previous.Outcome != current.Outcome || previous.Final != current.Final || previous.Usage != current.Usage || previous.Error != current.Error {
				return ErrTurnConflict
			}
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='read' WHERE to_kind=? AND to_id=? AND turn=? AND state='taken'", contract.PrincipalSession, session, output.Turn); err != nil {
			return err
		}
		inputs, posts, err := s.turnReplies(ctx, tx, event.Room, session, output.Turn)
		if err != nil {
			return err
		}
		body := event.Body.(contract.TurnEnded)
		body.Replies = room.FinalReplies(inputs, room.PostsForReplies(inputs, posts, session))
		event.Body = body
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		for _, reply := range body.Replies {
			if reply.Reply.Target.Kind != contract.PrincipalSession {
				continue
			}
			d := contract.Delivery{To: reply.Reply.Target, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: event.From, Home: reply.Reply.Home}, Wake: reply.Posted == ""}
			if err := insertDelivery(ctx, tx, seq, d); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Session(ctx context.Context, id contract.SessionID) (Session, error) {
	var session Session
	err := s.Read().QueryRowContext(ctx, "SELECT s.id,s.room,s.name,s.repo,s.profile,b.id FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.id=? AND s.closed_at IS NULL", id).Scan(&session.ID, &session.Room, &session.Name, &session.Repo, &session.Profile, &session.Box)
	return session, err
}

func (s *Store) RoomByName(ctx context.Context, name contract.RoomName) (contract.RoomID, error) {
	var id contract.RoomID
	err := s.Read().QueryRowContext(ctx, "SELECT id FROM rooms WHERE name=?", name).Scan(&id)
	return id, err
}

func (s *Store) SessionByName(ctx context.Context, room contract.RoomID, name string) (Session, error) {
	var session Session
	err := s.Read().QueryRowContext(ctx, "SELECT s.id,s.room,s.name,s.repo,s.profile,b.id FROM sessions s JOIN boxes b ON b.session=s.id WHERE s.room=? AND s.name=? AND s.closed_at IS NULL", room, name).Scan(&session.ID, &session.Room, &session.Name, &session.Repo, &session.Profile, &session.Box)
	return session, err
}

func insertDelivery(ctx context.Context, tx *sql.Tx, seq int64, d contract.Delivery) error {
	home := string(d.Reply.Home.Kind)
	if d.Reply.Home.Kind == contract.ConversationDM {
		home += ":" + string(d.Reply.Home.Session)
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state) VALUES(?,?,?,?,?,?,?,?,?,?,?)", d.To.Kind, d.To.Session, seq, d.Surface, d.Reply.Kind, d.Reply.Target.Kind, d.Reply.Target.Session, home, d.Reply.ReplyTo, d.Wake, "pending")
	return err
}

func (s *Store) Post(ctx context.Context, event contract.Event, deliveries []contract.Delivery) (int64, error) {
	var seq int64
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		seq, err = s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if err := insertDelivery(ctx, tx, seq, delivery); err != nil {
				return err
			}
		}
		return nil
	})
	return seq, err
}

// FeedInput describes a delivery selected for a box channel.
type FeedInput struct {
	Seq     contract.DeliverySeq
	Surface contract.Surface
	Reply   contract.ReplyChannel
	Queue   *contract.TurnQueue
}

// Dispatch atomically assigns pending inputs to the active turn, or starts one.
// replay includes previously taken inputs for a new channel connection.
func (s *Store) Dispatch(ctx context.Context, session contract.SessionID, turn contract.TurnID, started contract.Event, replay bool) (contract.TurnID, []FeedInput, error) {
	var active contract.TurnID
	var inputs []FeedInput
	err := s.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM turns WHERE session=? AND ended_at IS NULL", session).Scan(&active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT d.seq,d.surface,d.state,d.queue,e.id,d.turn,d.reply_kind,d.reply_target_kind,d.reply_target_id,d.reply_home,d.reply_to,d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_kind=? AND d.to_id=? AND (d.state IN ('pending','taken') OR (d.state='read' AND d.turn=? AND d.queue IS NULL AND ?)) ORDER BY d.seq", contract.PrincipalSession, session, active, replay)
		if err != nil {
			return err
		}
		type item struct {
			input FeedInput
			state string
			queue sql.NullString
			event contract.EventID
			turn  sql.NullString
			home  string
			wake  bool
		}
		var items []item
		for rows.Next() {
			var v item
			if err := rows.Scan(&v.input.Seq, &v.input.Surface, &v.state, &v.queue, &v.event, &v.turn, &v.input.Reply.Kind, &v.input.Reply.Target.Kind, &v.input.Reply.Target.Session, &v.home, &v.input.Reply.ReplyTo, &v.wake); err != nil {
				rows.Close()
				return err
			}
			kind, session, _ := strings.Cut(v.home, ":")
			v.input.Reply.Home = contract.Conversation{Kind: contract.ConversationKind(kind), Session: contract.SessionID(session)}
			if active == "" || v.wake || v.turn.Valid {
				items = append(items, v)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if active == "" {
			for _, item := range items {
				if item.wake {
					active = turn
					break
				}
			}
			if active == "" {
				return nil
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,?,?,?,?,?,?,?,?)", active, session, started.Time.UnixMilli(), "", "", 0, 0, 0, 0)
			if err != nil {
				return err
			}
			var ids []contract.EventID
			for _, v := range items {
				ids = append(ids, v.event)
			}
			started.Body = contract.TurnStarted{Session: session, Turn: active, Inputs: ids}
			if _, err := s.AppendEvent(tx, started); err != nil {
				return err
			}
		}
		for _, v := range items {
			assigned := v.turn.Valid && contract.TurnID(v.turn.String) == active
			if assigned && !replay {
				continue
			}
			if !assigned {
				var queue *contract.TurnQueue
				if active != turn {
					q := contract.QueueSteer
					queue = &q
				}
				_, err := tx.ExecContext(ctx, "UPDATE deliveries SET state='taken',turn=?,queue=? WHERE to_kind=? AND to_id=? AND seq=? AND state IN ('pending','taken')", active, queue, contract.PrincipalSession, session, v.input.Seq)
				if err != nil {
					return err
				}
				v.input.Queue = queue
			} else if v.queue.Valid {
				q := contract.TurnQueue(v.queue.String)
				v.input.Queue = &q
			}
			inputs = append(inputs, v.input)
		}
		return nil
	})
	return active, inputs, err
}
