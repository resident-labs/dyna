package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// StoredOperation keeps arguments encoded until the contract knows their kind.
type StoredOperation struct {
	Operation contract.Operation
	Args      json.RawMessage
}

const operationColumns = "id,kind,room,by_kind,by_id,class,state,card,args,created_at,expires_at,decision,result"

// Scan matches database/sql rows, whose destination types vary by column.
type operationRow interface{ Scan(...interface{}) error }

func scanOperation(row operationRow) (StoredOperation, error) {
	var r StoredOperation
	var card, decision, result []byte
	var created, expires int64
	o := &r.Operation
	if err := row.Scan(&o.ID, &o.Kind, &o.Room, &o.By.Kind, &o.By.Session, &o.Class, &o.State, &card, &r.Args, &created, &expires, &decision, &result); err != nil {
		return r, err
	}
	o.Created, o.Expires = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC()
	if err := json.Unmarshal(card, &o.Card); err != nil {
		return r, err
	}
	if decision != nil {
		o.Decision = new(contract.Decision)
		if err := json.Unmarshal(decision, o.Decision); err != nil {
			return r, err
		}
	}
	if result != nil {
		o.Result = new(contract.OpResult)
		if err := json.Unmarshal(result, o.Result); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *Store) Operation(ctx context.Context, id contract.OpID) (StoredOperation, error) {
	return scanOperation(s.read.QueryRowContext(ctx, "SELECT "+operationColumns+" FROM operations WHERE id=?", id))
}

// Operations lists records in a state. A nonzero due time limits expiry to that time.
func (s *Store) Operations(ctx context.Context, room contract.RoomID, state contract.OpState, due time.Time) ([]StoredOperation, error) {
	query := "SELECT " + operationColumns + " FROM operations"
	var rows *sql.Rows
	var err error
	if room != "" {
		rows, err = s.read.QueryContext(ctx, query+" WHERE room=? AND state=? ORDER BY created_at,id", room, state)
	} else if due.IsZero() {
		rows, err = s.read.QueryContext(ctx, query+" WHERE state=? ORDER BY created_at,id", state)
	} else {
		rows, err = s.read.QueryContext(ctx, query+" WHERE state=? AND expires_at<=? ORDER BY expires_at,id", state, due.UnixMilli())
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []StoredOperation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

// PendingOperation includes expired records; callers transition them before inserting.
func (s *Store) PendingOperation(tx *sql.Tx, room contract.RoomID, by contract.Principal, kind contract.OpKind, args json.RawMessage) (StoredOperation, error) {
	if tx != s.currentTx {
		return StoredOperation{}, errors.New("operation transaction is not the active writer transaction")
	}
	hash := sha256.Sum256(args)
	return scanOperation(tx.QueryRow("SELECT "+operationColumns+" FROM operations WHERE room=? AND by_kind=? AND by_id=? AND kind=? AND args_sha256=? AND state='pending'", room, by.Kind, by.Session, kind, hash[:]))
}

func (s *Store) operationEvent(tx *sql.Tx, event contract.Event, deliveries []contract.Delivery) error {
	if event.Body == nil {
		return nil
	}
	seq, err := s.AppendEvent(tx, event)
	if err != nil {
		return err
	}
	for _, delivery := range deliveries {
		if err := insertDelivery(context.Background(), tx, seq, delivery); err != nil {
			return err
		}
	}
	return nil
}

// InsertOperation requires a caller-owned Write transaction.
func (s *Store) InsertOperation(tx *sql.Tx, record StoredOperation, event contract.Event, deliveries []contract.Delivery) error {
	if tx != s.currentTx {
		return errors.New("operation transaction is not the active writer transaction")
	}
	o := record.Operation
	card, err := json.Marshal(o.Card)
	if err != nil {
		return err
	}
	if !json.Valid(record.Args) {
		return errors.New("invalid operation arguments JSON")
	}
	decision, err := json.Marshal(o.Decision)
	if err != nil {
		return err
	}
	result, err := json.Marshal(o.Result)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(record.Args)
	_, err = tx.Exec("INSERT INTO operations("+operationColumns+",args_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, o.Kind, o.Room, o.By.Kind, o.By.Session, o.Class, o.State, card, []byte(record.Args), o.Created.UnixMilli(), o.Expires.UnixMilli(), nullableJSON(o.Decision != nil, decision), nullableJSON(o.Result != nil, result), hash[:])
	if err != nil {
		return err
	}
	return s.operationEvent(tx, event, deliveries)
}

// TransitionOperation compares the current state and emits only on success.
func (s *Store) TransitionOperation(tx *sql.Tx, id contract.OpID, from, to contract.OpState, decision *contract.Decision, result *contract.OpResult, event contract.Event, deliveries []contract.Delivery) (bool, error) {
	if tx != s.currentTx {
		return false, errors.New("operation transaction is not the active writer transaction")
	}
	d, err := json.Marshal(decision)
	if err != nil {
		return false, err
	}
	r, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	res, err := tx.Exec("UPDATE operations SET state=?,decision=COALESCE(?,decision),result=COALESCE(?,result) WHERE id=? AND state=?", to, nullableJSON(decision != nil, d), nullableJSON(result != nil, r), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := s.operationEvent(tx, event, deliveries); err != nil {
		return false, err
	}
	return true, nil
}

func nullableJSON(present bool, data []byte) []byte {
	if !present {
		return nil
	}
	return data
}
