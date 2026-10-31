package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/resident-labs/dyna/contract"
	"time"
)

var ErrRuleNotFound = errors.New("no such rule")

func (s *Store) AddRule(ctx context.Context, req contract.RuleAddRequest, caller contract.Principal, event contract.Event) (int, error) {
	var number int
	err := s.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "INSERT INTO rules(room,number,text,by_kind,by_id,created_at) VALUES(?,(SELECT COALESCE(MAX(number),0)+1 FROM rules WHERE room=?),?,?,?,?) RETURNING number", req.Room, req.Room, req.Text, caller.Kind, caller.Session, event.Time.UnixMilli()).Scan(&number)
		if err != nil {
			return err
		}
		event.Body = contract.RuleAdded{Number: number, Text: req.Text}
		return s.ruleEvent(ctx, tx, event)
	})
	return number, err
}
func (s *Store) RemoveRule(ctx context.Context, req contract.RuleRemoveRequest, event contract.Event) error {
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE rules SET removed_at=? WHERE room=? AND number=? AND removed_at IS NULL", event.Time.UnixMilli(), req.Room, req.Number)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrRuleNotFound
		}
		return s.ruleEvent(ctx, tx, event)
	})
	return err
}
func (s *Store) EditRule(ctx context.Context, req contract.RuleEditRequest, event contract.Event) error {
	err := s.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE rules SET text=? WHERE room=? AND number=? AND removed_at IS NULL", req.Text, req.Room, req.Number)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n == 0 {
			if err == nil {
				err = ErrRuleNotFound
			}
			return err
		}
		return s.ruleEvent(ctx, tx, event)
	})
	return err
}
func (s *Store) ruleEvent(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	seq, err := s.AppendEvent(tx, event)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM sessions WHERE room=? AND closed_at IS NULL", event.Room)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id contract.SessionID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		delivery := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: id}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}
		if err := s.InsertDelivery(ctx, tx, seq, delivery); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) Rules(ctx context.Context, room contract.RoomID) ([]contract.RoomRule, error) {
	rows, err := s.Read().QueryContext(ctx, "SELECT number,text,by_kind,by_id,created_at FROM rules WHERE room=? AND removed_at IS NULL ORDER BY number", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []contract.RoomRule{}
	for rows.Next() {
		var rule contract.RoomRule
		var at int64
		if err := rows.Scan(&rule.Number, &rule.Text, &rule.By.Kind, &rule.By.Session, &at); err != nil {
			return nil, err
		}
		rule.At = time.UnixMilli(at).UTC()
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}
