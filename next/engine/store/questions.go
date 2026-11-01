package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var ErrNoAskGrant = errors.New("this session has no ask-owner grant")
var ErrQuestionNotWaiting = errors.New("question is no longer waiting")

// noOpenQuestion holds for a session s the owner owes no answer. A session that waits for one is not finished, however
// quiet it is, so whatever closes idle sessions needs this. Its box may still stop: the answer wakes it.
const noOpenQuestion = `NOT EXISTS (SELECT 1 FROM questions q WHERE q.session=s.id AND q.state='pending')`

// blockedOnQuestion holds for a session s with nothing safe left to do until the owner answers. Only this excuses a quiet
// session from check-ins: any other question leaves it work to do.
const blockedOnQuestion = `EXISTS (SELECT 1 FROM questions q WHERE q.session=s.id AND q.state='pending' AND q.blocked=1)`

const questionColumns = "id,room,session,text,state,answer,asked_at,answered_at,meanwhile,blocked"

func scanQuestion(row interface{ Scan(...any) error }) (contract.Question, error) {
	var q contract.Question
	var asked int64
	var answered sql.NullInt64
	if err := row.Scan(&q.ID, &q.Room, &q.Session, &q.Text, &q.State, &q.Answer, &asked, &answered, &q.Meanwhile, &q.Blocked); err != nil {
		return q, err
	}
	q.Asked = time.UnixMilli(asked).UTC()
	if answered.Valid {
		q.Answered = time.UnixMilli(answered.Int64).UTC()
	}
	return q, nil
}

func (s *Store) Question(ctx context.Context, id contract.QuestionID) (contract.Question, error) {
	return scanQuestion(s.read.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM questions WHERE id=?", id))
}

// AskQuestion records a question and its event. A repeated client ID returns the first question. The grant and the
// session's state are read in the write, so a close cannot slip between them.
func (s *Store) AskQuestion(ctx context.Context, q contract.Question, event contract.Event, clientID string) (contract.Question, error) {
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if clientID != "" {
			found, err := scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM questions WHERE id=(SELECT ref FROM client_replays WHERE method=? AND caller_kind=? AND caller_id=? AND client_id=?)", contract.MethodQuestionAsk, contract.PrincipalSession, q.Session, clientID))
			if err == nil {
				q = found
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		var granted bool
		err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM grants WHERE kind=? AND to_kind=? AND to_id=? AND revoked_at IS NULL AND expired_at IS NULL)", contract.GrantAskOwner, contract.PrincipalSession, q.Session).Scan(&granted)
		if err != nil {
			return err
		}
		if !granted {
			return ErrNoAskGrant
		}
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT state FROM sessions WHERE id=?", q.Session).Scan(&state); err != nil {
			return err
		}
		if state != "open" {
			return ErrSessionClosing
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO questions(id,room,session,text,state,asked_at,meanwhile,blocked) VALUES(?,?,?,?,?,?,?,?)", q.ID, q.Room, q.Session, q.Text, contract.QuestionStatePending, q.Asked.UnixMilli(), q.Meanwhile, q.Blocked); err != nil {
			return err
		}
		if clientID != "" {
			if _, err := tx.ExecContext(ctx, "INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES(?,?,?,?,?,?)", contract.MethodQuestionAsk, contract.PrincipalSession, q.Session, clientID, q.ID, q.Asked.UnixMilli()); err != nil {
				return err
			}
		}
		_, err = s.AppendEvent(tx, event)
		return err
	})
	return q, err
}

// AnswerQuestion moves a waiting question to answered and commits the delivery that wakes its session.
func (s *Store) AnswerQuestion(ctx context.Context, id contract.QuestionID, answer string, event contract.Event, deliveries []contract.Delivery) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE questions SET state=?,answer=?,answered_at=? WHERE id=? AND state=?", contract.QuestionStateAnswered, answer, event.Time.UnixMilli(), id, contract.QuestionStatePending)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrQuestionNotWaiting
		}
		seq, err := s.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if err := s.InsertDelivery(ctx, tx, seq, event.Body, delivery); err != nil {
				return err
			}
		}
		return nil
	})
}

// withdrawQuestions ends the waiting questions of closing sessions, in the write that started the close. Each question
// ends once, so its ID gives the event's. Nothing can be asked once the session is closing.
func (s *Store) withdrawQuestions(ctx context.Context, tx *sql.Tx, at time.Time) error {
	rows, err := tx.QueryContext(ctx, "UPDATE questions SET state=? WHERE state=? AND session IN (SELECT id FROM sessions WHERE state='closing') RETURNING id,room,session", contract.QuestionStateWithdrawn, contract.QuestionStatePending)
	if err != nil {
		return err
	}
	var events []contract.Event
	for rows.Next() {
		var body contract.QuestionWithdrawn
		var room contract.RoomID
		if err := rows.Scan(&body.ID, &room, &body.Session); err != nil {
			rows.Close()
			return err
		}
		events = append(events, contract.Event{ID: contract.EventID("ev_" + string(body.ID)), Time: at, Room: room, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		if _, err := s.AppendEvent(tx, event); err != nil {
			return err
		}
	}
	return nil
}

// answeredShown bounds the answered questions a list returns.
const answeredShown = 20

// Questions lists a room's waiting questions, oldest first, then its newest answered ones.
func (s *Store) Questions(ctx context.Context, room contract.RoomID) ([]contract.Question, error) {
	questions := []contract.Question{}
	for _, query := range []struct {
		order string
		state contract.QuestionState
		limit int
	}{{"asked_at,id", contract.QuestionStatePending, -1}, {"asked_at DESC,id DESC", contract.QuestionStateAnswered, answeredShown}} {
		rows, err := s.read.QueryContext(ctx, "SELECT "+questionColumns+" FROM questions WHERE room=? AND state=? ORDER BY "+query.order+" LIMIT ?", room, query.state, query.limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			q, err := scanQuestion(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			questions = append(questions, q)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return questions, nil
}
