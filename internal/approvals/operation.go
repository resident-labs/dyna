package approvals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dyna-agent/internal/authority"
)

// OperationKind identifies a registered gated operation.
type OperationKind string

// Decision gives the authority needed to run an operation.
type Decision struct{ Class Class }

// Card holds the existing request fields until the typed card migration.
type Card struct {
	Room, Repository, Server, Command, Tool, Title, Body string
}

// Operation declares the policy and execution of a gated operation.
type Operation[P any] struct {
	Kind        OperationKind
	Decide      func(context.Context, authority.Actor, P) (Decision, error)
	Card        func(P) Card
	Check       func(context.Context, authority.Actor, P) error
	Execute     func(context.Context, authority.Actor, P) (Result, error)
	PrepareHeld func(context.Context, authority.Actor, P) error
}

type registeredOperation struct {
	submit func(context.Context, authority.Actor, json.RawMessage) (Outcome, error)
}

// Outcome contains either a completed result or a held request.
type Outcome struct {
	Request Request
	Result  *Result
}

// Register installs an operation before the service starts.
func Register[P any](s *Service, op Operation[P]) {
	if op.Kind == "" || op.Decide == nil || op.Card == nil || op.Check == nil || op.Execute == nil {
		panic("incomplete approval operation")
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if _, exists := s.operations[op.Kind]; exists {
		panic("duplicate approval operation")
	}
	s.operations[op.Kind] = registeredOperation{submit: func(ctx context.Context, actor authority.Actor, raw json.RawMessage) (Outcome, error) {
		var payload P
		if err := json.Unmarshal(raw, &payload); err != nil {
			return Outcome{}, err
		}
		decision, err := op.Decide(ctx, actor, payload)
		if err != nil {
			return Outcome{}, err
		}
		if decision.Class != ClassAuto && decision.Class != ClassAgent && decision.Class != ClassOwner {
			return Outcome{}, errors.New("invalid approval class")
		}
		card := op.Card(payload)
		if actor.Role != authority.Owner && actor.Role != authority.Root && actor.Role != authority.Session {
			return Outcome{}, errors.New("invalid approval actor")
		}
		rec, err := s.roomFor(actor, card.Room)
		if err != nil {
			return Outcome{}, err
		}
		inline := actor.Role == authority.Owner || decision.Class == ClassAuto || decision.Class == ClassAgent && actor.Role == authority.Root
		if inline {
			actorName := Owner
			if actor.Role != authority.Owner {
				meta, e := s.session(actor.Session)
				if e != nil {
					return Outcome{}, e
				}
				actorName = meta.Address()
			}
			err := op.Check(ctx, actor, payload)
			var result Result
			if err == nil {
				result, err = op.Execute(ctx, actor, payload)
			}
			state := StateDone
			if err != nil {
				state = StateFailed
				result.Reason = err.Error()
				if result.ExitCode == nil || *result.ExitCode == 0 {
					code := 1
					result.ExitCode = &code
				}
			} else if result.ExitCode != nil && *result.ExitCode != 0 {
				state = StateFailed
			}
			auditCtx := context.WithoutCancel(ctx)
			auditErr := s.write(auditCtx, func(conn *sql.Conn) error {
				_, e := conn.ExecContext(auditCtx, `INSERT INTO approval_inline_log(room,actor,kind,target,state,result,exit_code,at) VALUES(?,?,?,?,?,?,?,?)`, rec.ID, actorName, op.Kind, card.Repository, state, result.Reason, result.ExitCode, s.now().UnixNano())
				return e
			})
			if auditErr != nil {
				return Outcome{}, errors.Join(err, auditErr)
			}
			return Outcome{Result: &result}, nil
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			return Outcome{}, err
		}
		hash := sha256.Sum256(raw)
		payloadHash := hex.EncodeToString(hash[:])
		if existing, err := s.duplicate(rec.ID, actor.Session, Kind(op.Kind), decision.Class, payloadHash); err != nil {
			return Outcome{}, err
		} else if existing.ID != "" {
			public, err := s.public(existing)
			return Outcome{Request: public}, err
		}
		if op.PrepareHeld != nil {
			if err := op.PrepareHeld(ctx, actor, payload); err != nil {
				return Outcome{}, err
			}
		}
		raw, err = json.Marshal(payload)
		if err != nil {
			return Outcome{}, err
		}
		card = op.Card(payload)
		r, err := s.create(ctx, actor, createInput{Room: rec.ID, Kind: Kind(op.Kind), Class: decision.Class, Payload: raw, PayloadHash: payloadHash, Repository: card.Repository, Server: card.Server, Command: card.Command, Tool: card.Tool, Title: card.Title, Body: card.Body})
		return Outcome{Request: r}, err
	}}
	s.executors[Kind(op.Kind)] = func(ctx context.Context, r Request) (Result, error) {
		var payload P
		if err := json.Unmarshal(r.Payload, &payload); err != nil {
			return Result{}, err
		}
		meta, err := s.session(r.Requester)
		if err != nil {
			return Result{}, err
		}
		role := authority.Session
		if meta.Role == "root" {
			role = authority.Root
		}
		actor := authority.Actor{Role: role, Session: r.Requester, Room: r.Room}
		if err := op.Check(ctx, actor, payload); err != nil {
			return Result{}, err
		}
		return op.Execute(ctx, actor, payload)
	}
}

// Submit is the entry point for a registered operation.
func Submit[P any](ctx context.Context, s *Service, actor authority.Actor, kind OperationKind, payload P) (Outcome, error) {
	s.execMu.Lock()
	op, ok := s.operations[kind]
	s.execMu.Unlock()
	if !ok {
		return Outcome{}, fmt.Errorf("unregistered approval operation %q", kind)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Outcome{}, err
	}
	return op.submit(ctx, actor, raw)
}

// LogEntry describes one inline or decided write.
type LogEntry struct {
	Time     time.Time     `json:"time"`
	Room     string        `json:"room"`
	Actor    string        `json:"actor"`
	Kind     OperationKind `json:"kind"`
	Target   string        `json:"target"`
	State    State         `json:"state,omitempty"`
	Action   Action        `json:"action,omitempty"`
	Detail   string        `json:"detail,omitempty"`
	ExitCode *int          `json:"exitCode,omitempty"`
}

func (s *Service) Log(actor authority.Actor, roomRef string, since time.Duration) ([]LogEntry, error) {
	rec, err := s.roomFor(actor, roomRef)
	if err != nil {
		return nil, err
	}
	db, err := s.db()
	if err != nil {
		return nil, err
	}
	cutoff := int64(0)
	if since > 0 {
		cutoff = s.now().Add(-since).UnixNano()
	}
	rows, err := db.Query(`SELECT at,room,actor,kind,target,state,'' AS action,result,exit_code FROM approval_inline_log WHERE room=? AND at>=?
UNION ALL SELECT d.at,r.room,d.actor,r.kind,r.repository,'',d.action,d.reason,NULL FROM approval_decisions d JOIN approval_requests r ON r.id=d.request AND r.room=d.room WHERE r.room=? AND d.at>=? ORDER BY 1 DESC`, rec.ID, cutoff, rec.ID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var at int64
		if err := rows.Scan(&at, &e.Room, &e.Actor, &e.Kind, &e.Target, &e.State, &e.Action, &e.Detail, &e.ExitCode); err != nil {
			return nil, err
		}
		e.Time = time.Unix(0, at).UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if meta, err := s.session(out[i].Actor); err == nil {
			out[i].Actor = meta.Address()
		}
	}
	return out, nil
}
