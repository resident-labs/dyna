// Package approve gates typed operations before their executors run.
package approve

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

var ErrForbidden = errors.New("forbidden")
var ErrInvalid = errors.New("invalid operation")
var ErrConflict = errors.New("operation is no longer pending")

type Entry struct {
	Ephemeral bool
	Global    bool
	Scope     contract.Scope
	Class     func(contract.OpArgs, contract.Principal) contract.Class
	Card      func(context.Context, contract.OpArgs, contract.Principal) contract.Card
	Check     func(context.Context, contract.OpArgs, contract.Principal) error
	Execute   func(context.Context, contract.OpID, contract.OpArgs, contract.Principal) contract.OpResult
	Decode    func(json.RawMessage) (contract.OpArgs, error)
}

type Service struct {
	Store   *store.Store
	Kinds   map[contract.OpKind]Entry
	Context context.Context
	Now     func() time.Time
	Expiry  time.Duration
	ID      func(string) (string, error)
	mu      sync.Mutex
	waiters map[contract.OpID]waiter
}

type waiter struct {
	ctx  context.Context
	done chan runResult
}

type runResult struct {
	op  contract.Operation
	err error
}

func New(st *store.Store, ctx context.Context, kinds map[contract.OpKind]Entry, now func() time.Time, expiry time.Duration, id func(string) (string, error)) *Service {
	return &Service{Store: st, Context: ctx, Kinds: kinds, Now: now, Expiry: expiry, ID: id, waiters: make(map[contract.OpID]waiter)}
}

func (s *Service) event(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
	id, err := s.ID("ev_")
	return contract.Event{ID: contract.EventID(id), Time: s.Now(), Room: room, From: from, Body: body}, err
}
func delivery(to contract.SessionID) []contract.Delivery {
	if to == "" {
		return nil
	}
	return []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: to}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}
}
func (s *Service) access(ctx context.Context, who contract.Principal, room contract.RoomID) error {
	if who.Kind == contract.PrincipalOwner {
		return nil
	}
	if who.Kind != contract.PrincipalSession {
		return ErrForbidden
	}
	session, err := s.Store.Session(ctx, who.Session)
	if err != nil {
		return err
	}
	if session.Room != room {
		return ErrForbidden
	}
	return nil
}
func (s *Service) Submit(ctx context.Context, who contract.Principal, input contract.Operation) (contract.Operation, error) {
	return s.submit(ctx, who, input, nil, "")
}

// SubmitWithClientID binds a retry key to the created or deduplicated operation.
func (s *Service) SubmitWithClientID(ctx context.Context, who contract.Principal, input contract.Operation, clientID string) (contract.Operation, error) {
	return s.submit(ctx, who, input, nil, clientID)
}

// SubmitEphemeral runs an owner action whose secrets must not enter its record.
func (s *Service) SubmitEphemeral(ctx context.Context, who contract.Principal, input contract.Operation, execute func(context.Context) contract.OpResult) (contract.Operation, error) {
	if execute == nil {
		return contract.Operation{}, ErrInvalid
	}
	return s.submit(ctx, who, input, execute, "")
}

func (s *Service) submit(ctx context.Context, who contract.Principal, input contract.Operation, execute func(context.Context) contract.OpResult, clientID string) (contract.Operation, error) {
	if err := s.access(ctx, who, input.Room); err != nil {
		return contract.Operation{}, err
	}
	if clientID != "" {
		previous, err := s.Store.ReadReplayOperation(ctx, who, clientID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return contract.Operation{}, err
		}
		if previous.Operation.ID != "" {
			return s.decodeReplay(previous, input)
		}
	}
	if input.Args == nil {
		return contract.Operation{}, ErrInvalid
	}
	if scoped, ok := input.Args.(interface{ OperationRoom() contract.RoomID }); ok && scoped.OperationRoom() != input.Room {
		return contract.Operation{}, ErrInvalid
	}
	kind := input.Args.OpKind()
	entry, ok := s.Kinds[kind]
	if !ok {
		return contract.Operation{}, ErrInvalid
	}
	if (input.Room == "") != entry.Global || (entry.Global && who.Kind != contract.PrincipalOwner) || entry.Ephemeral != (execute != nil) || (entry.Ephemeral && !entry.Global) {
		return contract.Operation{}, ErrForbidden
	}
	if execute != nil {
		entry.Execute = func(ctx context.Context, _ contract.OpID, _ contract.OpArgs, _ contract.Principal) contract.OpResult {
			return execute(ctx)
		}
	}
	checkErr := entry.Check(ctx, input.Args, who)
	args, err := json.Marshal(input.Args)
	if err != nil {
		return contract.Operation{}, err
	}
	class := entry.Class(input.Args, who)
	if entry.Global && class != contract.ClassOwner {
		return contract.Operation{}, ErrInvalid
	}
	immediate := who.Kind == contract.PrincipalOwner || class == contract.ClassAuto
	now := s.Now()
	id, err := s.ID("op_")
	if err != nil {
		return contract.Operation{}, err
	}
	op := contract.Operation{ID: contract.OpID(id), Kind: kind, Room: input.Room, By: who, Args: input.Args, Class: class, Card: entry.Card(ctx, input.Args, who), State: contract.OpPending, Created: now, Expires: now.Add(s.Expiry)}
	if immediate {
		op.State = contract.OpApproved
	}
	var request contract.Event
	if !immediate {
		request, err = s.event(op.Room, who, contract.OpRequested{ID: op.ID, Kind: op.Kind, By: who, Class: class, Card: op.Card, Expires: op.Expires})
		if err != nil {
			return contract.Operation{}, err
		}
	}
	var previous store.StoredOperation
	var replay bool
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		if clientID != "" {
			found, err := s.Store.ReplayOperation(tx, who, clientID)
			if err == nil {
				previous = found
				replay = true
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		found, err := s.Store.PendingOperation(tx, op.Room, who, op.Kind, args)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && !found.Operation.Expires.After(now) {
			if err := s.expireTx(tx, found.Operation); err != nil {
				return err
			}
		} else if err == nil {
			previous = found
			if clientID != "" {
				return s.Store.InsertOperationReplay(tx, who, clientID, found.Operation.ID)
			}
			return nil
		}
		if checkErr != nil {
			return checkErr
		}
		if err := s.Store.InsertOperation(tx, store.StoredOperation{Operation: op, Args: args}, request, nil); err != nil {
			return err
		}
		if clientID != "" {
			return s.Store.InsertOperationReplay(tx, who, clientID, op.ID)
		}
		return nil
	})
	if err != nil {
		return contract.Operation{}, err
	}
	if previous.Operation.ID != "" {
		if replay {
			return s.decodeReplay(previous, input)
		}
		previous.Operation.Args = input.Args
		return previous.Operation, nil
	}
	if !immediate {
		return op, nil
	}
	s.mu.Lock()
	resultCh := make(chan runResult, 1)
	s.waiters[op.ID] = waiter{ctx: ctx, done: resultCh}
	s.mu.Unlock()
	go s.start(op, entry)
	select {
	case result := <-resultCh:
		return result.op, result.err
	case <-ctx.Done():
		s.mu.Lock()
		select {
		case result := <-resultCh:
			s.mu.Unlock()
			return result.op, result.err
		default:
		}
		delete(s.waiters, op.ID)
		s.mu.Unlock()
		return op, nil
	}
}

func (s *Service) decodeReplay(record store.StoredOperation, input contract.Operation) (contract.Operation, error) {
	// The API authorized the submitted kind. Never return a different kind or room.
	if record.Operation.Kind != input.Kind || record.Operation.Room != input.Room {
		return contract.Operation{}, ErrForbidden
	}
	entry, ok := s.Kinds[record.Operation.Kind]
	if !ok {
		return contract.Operation{}, ErrInvalid
	}
	args, err := entry.Decode(record.Args)
	record.Operation.Args = args
	return record.Operation, err
}

func (s *Service) Decide(ctx context.Context, who contract.Principal, id contract.OpID, state contract.OpState, reason string) error {
	record, err := s.Store.Operation(ctx, id)
	if err != nil {
		return err
	}
	op := record.Operation
	if err := s.access(ctx, who, op.Room); err != nil {
		return err
	}
	// Only the owner decides; a session may only withdraw its own request.
	if who.Kind != contract.PrincipalOwner && (state != contract.OpWithdrawn || who != op.By) {
		return ErrForbidden
	}
	var entry Entry
	if state == contract.OpApproved {
		var ok bool
		entry, ok = s.Kinds[op.Kind]
		if !ok {
			return ErrInvalid
		}
		op.Args, err = entry.Decode(record.Args)
		if err != nil {
			return err
		}
	}
	decision := contract.Decision{By: who, Approved: state == contract.OpApproved, Reason: reason, At: s.Now()}
	event, err := s.event(op.Room, who, contract.OpDecided{ID: op.ID, State: state, Decision: decision})
	if err != nil {
		return err
	}
	var notify []contract.Delivery
	if state == contract.OpDenied || state == contract.OpWithdrawn && who != op.By {
		notify = delivery(op.By.Session)
	}
	expired := false
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		if !op.Expires.After(s.Now()) {
			expired = true
			return s.expireTx(tx, op)
		}
		changed, err := s.Store.TransitionOperation(tx, id, contract.OpPending, state, &decision, nil, event, notify)
		if err != nil {
			return err
		}
		if !changed {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	if expired {
		return ErrConflict
	}
	if state == contract.OpApproved {
		op.State = state
		op.Decision = &decision
		go s.start(op, entry)
	}
	return nil
}

func (s *Service) expireTx(tx *sql.Tx, op contract.Operation) error {
	d := contract.Decision{By: contract.Principal{Kind: contract.PrincipalEngine}, At: s.Now(), Reason: "expired"}
	event, err := s.event(op.Room, d.By, contract.OpDecided{ID: op.ID, State: contract.OpExpired, Decision: d})
	if err != nil {
		return err
	}
	_, err = s.Store.TransitionOperation(tx, op.ID, contract.OpPending, contract.OpExpired, &d, nil, event, delivery(op.By.Session))
	return err
}

// ExpireKind invalidates pending cards whose facts may have changed across a restart.
func (s *Service) ExpireKind(ctx context.Context, kind contract.OpKind) error {
	ops, err := s.Store.Operations(ctx, "", contract.OpPending, time.Time{})
	if err != nil {
		return err
	}
	for _, record := range ops {
		if record.Operation.Kind == kind {
			if err := s.Store.Write(ctx, func(tx *sql.Tx) error { return s.expireTx(tx, record.Operation) }); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Expire(ctx context.Context) error {
	ops, err := s.Store.Operations(ctx, "", contract.OpPending, s.Now())
	if err != nil {
		return err
	}
	for _, record := range ops {
		if err := s.Store.Write(ctx, func(tx *sql.Tx) error { return s.expireTx(tx, record.Operation) }); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) start(op contract.Operation, entry Entry) {
	err := s.run(op, entry)
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.waiters[op.ID]; ok {
		delete(s.waiters, op.ID)
		w.done <- runResult{op: op, err: err}
	} else {
		slog.Error("operation execution failed", "operation", op.ID, "error", err)
	}
}
func (s *Service) run(op contract.Operation, entry Entry) error {
	err := s.Store.Write(s.Context, func(tx *sql.Tx) error {
		changed, err := s.Store.TransitionOperation(tx, op.ID, contract.OpApproved, contract.OpRunning, nil, nil, contract.Event{}, nil)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("operation %s is not approved", op.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}

	result := contract.OpResult{}
	if err := entry.Check(s.Context, op.Args, op.By); err != nil {
		result.Error = "stale: " + err.Error()
		result.Cause = contract.CauseStale
	} else {
		result = entry.Execute(s.Context, op.ID, op.Args, op.By)
	}
	err = s.finish(s.Context, op, result)
	if err != nil {
		err = s.finish(context.WithoutCancel(s.Context), op, result)
	}
	return err
}
func (s *Service) finish(ctx context.Context, op contract.Operation, result contract.OpResult) error {
	state := contract.OpDone
	if result.Error != "" || result.ExitCode != 0 {
		state = contract.OpFailed
	}
	event, err := s.event(op.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.OpRan{ID: op.ID, State: state, Result: result})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	waiting, present := s.waiters[op.ID]
	if present && waiting.ctx.Err() != nil {
		delete(s.waiters, op.ID)
		present = false
	}
	var notify []contract.Delivery
	if !present {
		notify = delivery(op.By.Session)
	}
	err = s.Store.Write(ctx, func(tx *sql.Tx) error {
		changed, err := s.Store.TransitionOperation(tx, op.ID, contract.OpRunning, state, nil, &result, event, notify)
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("operation %s is not claimed", op.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if present {
		delete(s.waiters, op.ID)
		op.State = state
		op.Result = &result
		waiting.done <- runResult{op: op}
	}
	return nil
}
func (s *Service) Recover(ctx context.Context) error {
	running, err := s.Store.Operations(ctx, "", contract.OpRunning, time.Time{})
	if err != nil {
		return err
	}
	for _, record := range running {
		message := "interrupted: outcome unknown"
		if record.Operation.Kind == "pr.open" {
			message += "; check whether the PR exists before opening another"
		}
		if err := s.finish(ctx, record.Operation, contract.OpResult{Error: message, Cause: contract.CauseInterrupted}); err != nil {
			return err
		}
	}
	approved, err := s.Store.Operations(ctx, "", contract.OpApproved, time.Time{})
	if err != nil {
		return err
	}
	for _, record := range approved {
		entry, ok := s.Kinds[record.Operation.Kind]
		if !ok {
			return ErrInvalid
		}
		record.Operation.Args, err = entry.Decode(record.Args)
		if err != nil {
			return err
		}
		go s.start(record.Operation, entry)
	}
	return nil
}
func (s *Service) List(ctx context.Context, who contract.Principal, room contract.RoomID) ([]contract.Operation, error) {
	if err := s.access(ctx, who, room); err != nil {
		return nil, err
	}
	records, err := s.Store.Operations(ctx, room, contract.OpPending, time.Time{})
	if err != nil {
		return nil, err
	}
	var out []contract.Operation
	for _, record := range records {
		entry, ok := s.Kinds[record.Operation.Kind]
		if !ok {
			return nil, errors.New("unknown operation kind")
		}
		record.Operation.Args, err = entry.Decode(record.Args)
		if err != nil {
			return nil, err
		}
		out = append(out, record.Operation)
	}
	return out, nil
}
