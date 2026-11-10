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
	var room sql.NullString
	o := &r.Operation
	if err := row.Scan(&o.ID, &o.Kind, &room, &o.By.Kind, &o.By.Session, &o.Class, &o.State, &card, &r.Args, &created, &expires, &decision, &result); err != nil {
		return r, err
	}
	o.Room = contract.RoomID(room.String)
	o.Created = time.UnixMilli(created).UTC()
	if expires != 0 { // zero means the request never expires
		o.Expires = time.UnixMilli(expires).UTC()
	}
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

const replayOperationQuery = "SELECT " + operationColumns + " FROM operations WHERE id=(SELECT ref FROM client_replays WHERE method=? AND caller_kind=? AND caller_id=? AND client_id=?)"

// ReadReplayOperation finds a caller's keyed operation outside a write transaction.
func (s *Store) ReadReplayOperation(ctx context.Context, caller contract.Principal, clientID string) (StoredOperation, error) {
	record, err := scanOperation(s.read.QueryRowContext(ctx, replayOperationQuery, contract.MethodOpSubmit, caller.Kind, caller.Session, clientID))
	if errors.Is(err, sql.ErrNoRows) {
		return StoredOperation{}, nil
	}
	return record, err
}

func (s *Store) InsertOperationReplay(tx *sql.Tx, caller contract.Principal, clientID string, op contract.OpID, now time.Time) error {
	_, err := tx.Exec("INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES(?,?,?,?,?,?)", contract.MethodOpSubmit, caller.Kind, caller.Session, clientID, op, now.UnixMilli())
	return err
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
		rows, err = s.read.QueryContext(ctx, query+" WHERE state=? AND expires_at>0 AND expires_at<=? ORDER BY expires_at,id", state, due.UnixMilli())
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
	return scanOperation(tx.QueryRow("SELECT "+operationColumns+" FROM operations WHERE room IS ? AND by_kind=? AND by_id=? AND kind=? AND args_sha256=? AND state='pending'", sql.NullString{String: string(room), Valid: room != ""}, by.Kind, by.Session, kind, hash[:]))
}

func (s *Store) operationEvent(tx *sql.Tx, event contract.Event, deliveries []contract.Delivery) error {
	if event.Body == nil {
		return nil
	}
	seq, err := s.AppendEvent(tx, event)
	if err != nil {
		return err
	}
	if ran, ok := event.Body.(contract.OpRan); ok && ran.Result.OutputFile != "" {
		if _, err := tx.Exec("INSERT OR IGNORE INTO event_files(seq,sha256,media) VALUES(?,?,?)", seq, ran.Result.OutputFile, "text/plain"); err != nil {
			return err
		}
	}
	if ran, ok := event.Body.(contract.OpRan); ok && ran.Result.OutFiles != "" {
		if _, err := tx.Exec("INSERT OR IGNORE INTO event_files(seq,sha256,media) VALUES(?,?,?)", seq, ran.Result.OutFiles, "application/x-tar"); err != nil {
			return err
		}
	}
	for _, delivery := range deliveries {
		if err := s.InsertDelivery(context.Background(), tx, seq, event.Body, delivery); err != nil {
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
	var expires int64
	if !o.Expires.IsZero() {
		expires = o.Expires.UnixMilli()
	}
	_, err = tx.Exec("INSERT INTO operations("+operationColumns+",args_sha256) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, o.Kind, sql.NullString{String: string(o.Room), Valid: o.Room != ""}, o.By.Kind, o.By.Session, o.Class, o.State, card, []byte(record.Args), o.Created.UnixMilli(), expires, nullableJSON(o.Decision != nil, decision), nullableJSON(o.Result != nil, result), hash[:])
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

// OperationExpiry constructs the decision and event only when expiry is needed.
type OperationExpiry func(contract.Operation) (contract.Decision, contract.Event, []contract.Delivery, error)

func (s *Store) expireOperation(tx *sql.Tx, op contract.Operation, expiry OperationExpiry) error {
	d, event, deliveries, err := expiry(op)
	if err != nil {
		return err
	}
	_, err = s.TransitionOperation(tx, op.ID, contract.OpPending, contract.OpExpired, &d, nil, event, deliveries)
	return err
}

func (s *Store) SubmitOperation(ctx context.Context, record StoredOperation, request contract.Event, clientID string, now time.Time, checkErr error, expiry OperationExpiry) (previous StoredOperation, replay bool, err error) {
	op := record.Operation
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if clientID != "" {
			found, err := scanOperation(tx.QueryRow(replayOperationQuery, contract.MethodOpSubmit, op.By.Kind, op.By.Session, clientID))
			if err == nil {
				previous = found
				replay = true
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		found, err := s.PendingOperation(tx, op.Room, op.By, op.Kind, record.Args)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !found.Operation.Expires.IsZero() && !found.Operation.Expires.After(now) {
			if err := s.expireOperation(tx, found.Operation, expiry); err != nil {
				return err
			}
		} else if err == nil {
			previous = found
			if clientID != "" {
				return s.InsertOperationReplay(tx, op.By, clientID, found.Operation.ID, now)
			}
			return nil
		}
		if checkErr != nil {
			return checkErr
		}
		var deliveries []contract.Delivery
		if to := record.Operation.Decider; to != "" {
			deliveries = []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}
		}
		if err := s.InsertOperation(tx, record, request, deliveries); err != nil {
			return err
		}
		if clientID != "" {
			return s.InsertOperationReplay(tx, op.By, clientID, op.ID, now)
		}
		return nil
	})
	return
}

func (s *Store) DecideOperation(ctx context.Context, op contract.Operation, state contract.OpState, decision contract.Decision, event contract.Event, deliveries []contract.Delivery, now func() time.Time, expiry OperationExpiry) (expired, changed bool, err error) {
	err = s.Write(ctx, func(tx *sql.Tx) error {
		if !op.Expires.IsZero() && !op.Expires.After(now()) {
			expired = true
			return s.expireOperation(tx, op, expiry)
		}
		var err error
		changed, err = s.TransitionOperation(tx, op.ID, contract.OpPending, state, &decision, nil, event, deliveries)
		return err
	})
	return
}

func (s *Store) ExpireOperation(ctx context.Context, op contract.Operation, expiry OperationExpiry) error {
	return s.Write(ctx, func(tx *sql.Tx) error { return s.expireOperation(tx, op, expiry) })
}

// withdrawGrantRequests ends the pending host exec grant requests of sessions that are closing, whether they asked or were asked for.
func (s *Store) withdrawGrantRequests(ctx context.Context, tx *sql.Tx, at time.Time) error {
	engine := contract.Principal{Kind: contract.PrincipalEngine}
	decision := contract.Decision{By: engine, Reason: "session closed", At: at}
	data, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE operations SET state=?1, decision=?2 WHERE state='pending' AND kind='hostexec.grant' AND (
		(by_kind='session' AND by_id IN (SELECT id FROM sessions WHERE state='closing'))
		OR json_extract(CAST(args AS TEXT),'$.to.session') IN (SELECT id FROM sessions WHERE state='closing')) RETURNING id,COALESCE(room,'')`,
		contract.OpWithdrawn, data)
	if err != nil {
		return err
	}
	var events []contract.Event
	for rows.Next() {
		var id contract.OpID
		var room contract.RoomID
		if err := rows.Scan(&id, &room); err != nil {
			rows.Close()
			return err
		}
		events = append(events, contract.Event{ID: contract.EventID("ev_" + string(id) + "_closed"), Time: at, Room: room, From: engine, Body: contract.OpDecided{ID: id, State: contract.OpWithdrawn, Decision: decision}})
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

func (s *Store) ChangeOperation(ctx context.Context, id contract.OpID, from, to contract.OpState, result *contract.OpResult, event contract.Event, deliveries []contract.Delivery) (changed bool, err error) {
	err = s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		changed, err = s.TransitionOperation(tx, id, from, to, nil, result, event, deliveries)
		return err
	})
	return
}
