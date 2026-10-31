package engine

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) ruleWriter(ctx context.Context, caller contract.Principal, room contract.RoomID) *contract.Error {
	if problem := s.roomAccess(ctx, caller, room); problem != nil {
		return problem
	}
	if caller.Kind == contract.PrincipalOwner {
		return nil
	}
	tree, err := s.store.RoomTree(ctx, room)
	if err != nil {
		return failure(err)
	}
	if tree.Root() != caller.Session {
		return forbidden()
	}
	return nil
}

func (s *Service) RuleAdd(ctx context.Context, caller contract.Principal, req contract.RuleAddRequest) (contract.RuleAddResponse, *contract.Error) {
	var response contract.RuleAddResponse
	if problem := s.ruleWriter(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	if strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > 2000 {
		return response, invalid("rule text must contain 1 to 2000 characters")
	}
	event, err := s.event(req.Room, caller, contract.RuleAdded{Text: req.Text})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "INSERT INTO rules(room,number,text,by_kind,by_id,created_at) VALUES(?,(SELECT COALESCE(MAX(number),0)+1 FROM rules WHERE room=?),?,?,?,?) RETURNING number", req.Room, req.Room, req.Text, caller.Kind, caller.Session, event.Time.UnixMilli()).Scan(&response.Number)
		if err != nil {
			return err
		}
		event.Body = contract.RuleAdded{Number: response.Number, Text: req.Text}
		return s.ruleEvent(ctx, tx, event)
	})
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) RuleRemove(ctx context.Context, caller contract.Principal, req contract.RuleRemoveRequest) (contract.RuleRemoveResponse, *contract.Error) {
	var response contract.RuleRemoveResponse
	if problem := s.ruleWriter(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	if req.Number < 1 {
		return response, invalid("rule number must be positive")
	}
	event, err := s.event(req.Room, caller, contract.RuleRemoved{Number: req.Number})
	if err != nil {
		return response, failure(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE rules SET removed_at=? WHERE room=? AND number=? AND removed_at IS NULL", event.Time.UnixMilli(), req.Room, req.Number)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return s.ruleEvent(ctx, tx, event)
	})
	if err == sql.ErrNoRows {
		return response, invalid("no such rule")
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) ruleEvent(ctx context.Context, tx *sql.Tx, event contract.Event) error {
	seq, err := s.store.AppendEvent(tx, event)
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
		if err := s.store.InsertDelivery(ctx, tx, seq, delivery); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Service) RuleList(ctx context.Context, caller contract.Principal, req contract.RuleListRequest) (contract.RuleListResponse, *contract.Error) {
	var response contract.RuleListResponse
	if problem := s.roomAccess(ctx, caller, req.Room); problem != nil {
		return response, problem
	}
	rows, err := s.store.Read().QueryContext(ctx, "SELECT number,text,by_kind,by_id,created_at FROM rules WHERE room=? AND removed_at IS NULL ORDER BY number", req.Room)
	if err != nil {
		return response, failure(err)
	}
	defer rows.Close()
	response.Rules = []contract.RoomRule{}
	for rows.Next() {
		var rule contract.RoomRule
		var at int64
		if err := rows.Scan(&rule.Number, &rule.Text, &rule.By.Kind, &rule.By.Session, &at); err != nil {
			return response, failure(err)
		}
		rule.At = time.UnixMilli(at).UTC()
		response.Rules = append(response.Rules, rule)
	}
	if err := rows.Err(); err != nil {
		return response, failure(err)
	}
	return response, nil
}
