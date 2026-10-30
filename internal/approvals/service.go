package approvals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife"
)

// Service owns approval routing, persistence and delivery.
type Service struct {
	Rooms       *room.Store
	SessionRoot string
	Now         func() time.Time

	mu      sync.Mutex
	changes map[string]chan struct{}

	execMu    sync.Mutex
	executors map[Kind]ExecuteFunc
	running   map[string]bool
	runCtx    context.Context
}

// New pairs approvals with the room and session stores.
func New(rooms *room.Store, sessionRoot string) *Service {
	return &Service{Rooms: rooms, SessionRoot: sessionRoot, changes: map[string]chan struct{}{}, executors: map[Kind]ExecuteFunc{}, running: map[string]bool{}, runCtx: context.Background()}
}

// sessionMeta is the stored session ancestry and identity that approvals need.
// Keeping this small avoids coupling the approval core to harness integrations.
type sessionMeta struct {
	ID            string
	Name          string
	Role          string
	RoomID        string
	Room          string
	ParentSession string
	Closed        time.Time
	Closing       *sessionlife.Closing
}

func (m sessionMeta) Address() string       { return m.Room + ":" + m.Name }
func (m sessionMeta) EffectiveName() string { return m.Name }

func (s *Service) session(id string) (sessionMeta, error) {
	var meta sessionMeta
	err := sessionlife.Load(s.SessionRoot, id, &meta)
	return meta, err
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) roomFor(actor authority.Actor, ref string) (room.Record, error) {
	if ref == "" {
		ref = actor.Room
	}
	rec, err := s.Rooms.Resolve(ref)
	if err != nil {
		return room.Record{}, err
	}
	if actor.Role != authority.Owner && actor.Room != rec.ID && actor.Room != rec.Name {
		return room.Record{}, errors.New("this caller can use approvals only in its own room")
	}
	return rec, nil
}

// Create files a kind-independent request for an authenticated agent.
func (s *Service) Create(ctx context.Context, actor authority.Actor, in CreateInput) (Request, error) {
	if actor.Role != authority.Session && actor.Role != authority.Root {
		return Request{}, errors.New("only an agent session can request approval")
	}
	if in.Kind == "" || in.Class != ClassAgent && in.Class != ClassOwner || !json.Valid(in.Payload) {
		return Request{}, errors.New("an approval needs a kind, agent or owner class, and a JSON payload")
	}
	rec, err := s.roomFor(actor, in.Room)
	if err != nil {
		return Request{}, err
	}
	requester, err := s.session(actor.Session)
	if err != nil {
		return Request{}, err
	}
	if requester.RoomID != rec.ID {
		return Request{}, errors.New("the requester belongs to another room")
	}
	hash := sha256.Sum256(in.Payload)
	r := Request{
		Room: rec.ID, Requester: requester.ID, Kind: in.Kind, Class: in.Class,
		Payload: append(json.RawMessage(nil), in.Payload...), PayloadHash: hex.EncodeToString(hash[:]),
		Repository: in.Repository, Server: in.Server, Command: in.Command, Tool: in.Tool, Title: in.Title, Body: in.Body,
		State: StatePending, atID: requester.ParentSession,
	}
	if r.atID == "" || in.Class == ClassOwner {
		r.atID = Owner
	}
	now := s.now().UTC()
	expiry := rec.ApprovalExpiry
	if expiry == 0 {
		expiry = DefaultExpiry
	}
	r.CreatedAt, r.UpdatedAt, r.ExpiresAt = now, now, now.Add(expiry)
	auto := in.Auto && in.Class == ClassAgent || in.Class == ClassAgent && actor.Role == authority.Root
	if auto {
		r.State = StateApproved
		r.Result = &Result{Actor: requester.Address()}
	}
	if existing, err := s.duplicate(r.Room, r.Requester, r.Kind, r.Class, r.PayloadHash); err != nil {
		return Request{}, err
	} else if existing.ID != "" {
		if err := s.postRequested(existing); err != nil {
			return Request{}, err
		}
		return s.public(existing)
	}
	err = s.write(ctx, func(conn *sql.Conn) error {
		if err := insertRequest(ctx, conn, &r); err != nil {
			return err
		}
		if auto {
			reason := "root"
			if in.Auto {
				reason = "routine PR write"
			}
			return insertDecision(ctx, conn, r.ID, requester.ID, ActionAuto, reason, now)
		}
		return nil
	})
	if err != nil {
		return Request{}, err
	}
	if auto {
		reason := "root"
		if in.Auto {
			reason = "routine PR write"
		}
		err = s.postDecided(r, requester.Address(), ActionAuto, reason, "")
	} else {
		err = s.postRequested(r)
	}
	if err != nil {
		return Request{}, err
	}
	s.changed(r.Room, r.ID)
	if auto {
		s.trigger(r)
	}
	return s.public(r)
}

// Decide applies approve, deny or pass as the current holder.
func (s *Service) Decide(ctx context.Context, actor authority.Actor, in DecisionInput) (Request, error) {
	rec, err := s.roomFor(actor, in.Room)
	if err != nil {
		return Request{}, err
	}
	r, err := s.get(rec.ID, in.RequestID)
	if err != nil {
		return Request{}, err
	}
	if r.State != StatePending {
		return Request{}, ErrNotPending
	}
	if !s.now().Before(r.ExpiresAt) {
		if err := s.expire(ctx, r); err != nil {
			return Request{}, err
		}
		return Request{}, ErrNotPending
	}
	actorID := actor.Session
	actorName := ""
	if actor.Role == authority.Owner {
		actorID, actorName = Owner, Owner
	} else {
		info, err := s.session(actor.Session)
		if err != nil {
			return Request{}, err
		}
		actorName = info.Address()
	}
	if actorID == "" || actorID != r.atID || r.Class == ClassOwner && actor.Role != authority.Owner {
		return Request{}, ErrForbidden
	}
	if in.Action == ActionDeny && strings.TrimSpace(in.Reason) == "" {
		return Request{}, errors.New("deny requires a reason")
	}
	next := r.atID
	state := StateApproved
	var result *Result
	switch in.Action {
	case ActionApprove:
		result = &Result{Actor: actorName}
	case ActionDeny:
		state = StateDenied
		result = &Result{Actor: actorName, Reason: strings.TrimSpace(in.Reason)}
	case ActionPass:
		if actor.Role == authority.Owner {
			return Request{}, errors.New("the owner cannot pass an approval higher")
		}
		state = StatePending
		next, err = s.nextHolder(r.Room, actorID)
		if err != nil {
			return Request{}, err
		}
	default:
		return Request{}, errors.New("approval action must be approve, deny or pass")
	}
	now := s.now().UTC()
	err = s.write(ctx, func(conn *sql.Conn) error {
		resultJSON, err := writeResult(Request{Result: result})
		if err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx, `UPDATE approval_requests SET state=?,at=?,result=?,updated_at=? WHERE room=? AND id=? AND state='pending' AND at=?`, state, next, resultJSON, now.UnixNano(), r.Room, r.ID, r.atID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotPending
		}
		return insertDecision(ctx, conn, r.ID, actorID, in.Action, strings.TrimSpace(in.Reason), now)
	})
	if err != nil {
		return Request{}, err
	}
	r.State, r.atID, r.At, r.Result, r.UpdatedAt = state, next, next, result, now
	if err := s.postDecided(r, actorName, in.Action, strings.TrimSpace(in.Reason), next); err != nil {
		return Request{}, err
	}
	if state == StateDenied {
		if err := s.postResult(r, false); err != nil {
			return Request{}, err
		}
	}
	s.changed(r.Room, r.ID)
	if state == StateApproved {
		s.trigger(r)
	}
	return s.public(r)
}

// Cancel lets the requester cancel its own pending request.
func (s *Service) Cancel(ctx context.Context, actor authority.Actor, roomRef, id string) (Request, error) {
	rec, err := s.roomFor(actor, roomRef)
	if err != nil {
		return Request{}, err
	}
	r, err := s.get(rec.ID, id)
	if err != nil {
		return Request{}, err
	}
	if actor.Session == "" || actor.Session != r.Requester {
		return Request{}, errors.New("only the requester can cancel an approval")
	}
	requester, err := s.session(r.Requester)
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	result := &Result{Actor: requester.Address(), Reason: "cancelled by requester"}
	if err := s.finish(ctx, r, StateCancelled, result, now); err != nil {
		return Request{}, err
	}
	r.State, r.Result, r.UpdatedAt = StateCancelled, result, now
	if err := s.postResult(r, false); err != nil {
		return Request{}, err
	}
	s.changed(r.Room, r.ID)
	return s.public(r)
}

func (s *Service) finish(ctx context.Context, r Request, state State, result *Result, now time.Time) error {
	return s.write(ctx, func(conn *sql.Conn) error {
		resultJSON, err := writeResult(Request{Result: result})
		if err != nil {
			return err
		}
		res, err := conn.ExecContext(ctx, `UPDATE approval_requests SET state=?,result=?,updated_at=? WHERE room=? AND id=? AND state='pending'`, state, resultJSON, now.UnixNano(), r.Room, r.ID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n != 1 {
			return ErrNotPending
		}
		return err
	})
}

// List returns the room's pending request summaries.
func (s *Service) List(actor authority.Actor, roomRef string) ([]Summary, error) {
	rec, err := s.roomFor(actor, roomRef)
	if err != nil {
		return nil, err
	}
	requests, err := s.pending(rec.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(requests))
	for _, r := range requests {
		at, err := s.holderName(r.atID)
		if err != nil {
			return nil, err
		}
		out = append(out, Summary{ID: r.ID, State: r.State, At: at})
	}
	return out, nil
}

// Get returns one request to its requester or the owner.
func (s *Service) Get(actor authority.Actor, roomRef, id string) (Request, error) {
	rec, err := s.roomFor(actor, roomRef)
	if err != nil {
		return Request{}, err
	}
	r, err := s.get(rec.ID, id)
	if err != nil {
		return Request{}, err
	}
	if actor.Role != authority.Owner && actor.Session != r.Requester {
		return Request{}, ErrForbidden
	}
	return s.public(r)
}

// Wait waits for a final result or returns the pending request at timeout.
func (s *Service) Wait(ctx context.Context, actor authority.Actor, roomRef, id string, timeout time.Duration) (Request, error) {
	rec, err := s.roomFor(actor, roomRef)
	if err != nil {
		return Request{}, err
	}
	if timeout == 0 {
		timeout = rec.ApprovalWait
		if timeout == 0 {
			timeout = DefaultWait
		}
	}
	if timeout < 0 {
		return Request{}, errors.New("approval wait must not be negative")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		key := rec.ID + "\x00" + id
		ch := s.changeChan(key)
		r, err := s.get(rec.ID, id)
		if err != nil {
			return Request{}, err
		}
		if actor.Role != authority.Owner && actor.Session != r.Requester {
			return Request{}, ErrForbidden
		}
		if r.State == StatePending && !s.now().Before(r.ExpiresAt) {
			if err := s.expire(ctx, r); err != nil && !errors.Is(err, ErrNotPending) {
				return Request{}, err
			}
			continue
		}
		if r.Final() {
			return s.public(r)
		}
		select {
		case <-ch:
		case <-timer.C:
			return s.public(r)
		case <-ctx.Done():
			return Request{}, ctx.Err()
		}
	}
}

// ExpireDue expires every pending request whose deadline has passed.
func (s *Service) ExpireDue(ctx context.Context) error {
	requests, err := s.due(s.now())
	if err != nil {
		return err
	}
	for _, r := range requests {
		if err := s.expire(ctx, r); err != nil && !errors.Is(err, ErrNotPending) {
			return err
		}
	}
	return nil
}

func (s *Service) expire(ctx context.Context, r Request) error {
	now := s.now().UTC()
	result := &Result{Reason: "approval expired"}
	if err := s.finish(ctx, r, StateExpired, result, now); err != nil {
		return err
	}
	r.State, r.Result, r.UpdatedAt = StateExpired, result, now
	if err := s.postResult(r, false); err != nil {
		return err
	}
	s.changed(r.Room, r.ID)
	return nil
}

// CloseSession cancels the closing requester's requests and moves requests it
// held to the already selected open heir, or to the owner.
func (s *Service) CloseSession(ctx context.Context, roomID, closingID, heirID string) error {
	info, err := s.session(closingID)
	if err != nil {
		return err
	}
	requests, err := s.pending(roomID)
	if err != nil {
		return err
	}
	for _, r := range requests {
		switch {
		case r.Requester == info.ID:
			result := &Result{Actor: info.Address(), Reason: "session closed"}
			if err := s.finish(ctx, r, StateCancelled, result, s.now().UTC()); err != nil && !errors.Is(err, ErrNotPending) {
				return err
			} else if err == nil {
				r.State, r.Result = StateCancelled, result
				if err := s.postResult(r, true); err != nil {
					return err
				}
				s.changed(r.Room, r.ID)
			}
		case r.atID == info.ID:
			next := heirID
			if next == "" {
				next = Owner
			}
			if err := s.write(ctx, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `UPDATE approval_requests SET at=?,updated_at=? WHERE room=? AND id=? AND state='pending' AND at=?`, next, s.now().UnixNano(), r.Room, r.ID, info.ID)
				return err
			}); err != nil {
				return err
			}
			r.atID, r.At = next, next
			if err := s.postRequested(r); err != nil {
				return err
			}
			s.changed(r.Room, r.ID)
		}
	}
	return nil
}

func (s *Service) nextHolder(roomID, sessionID string) (string, error) {
	info, err := s.session(sessionID)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{sessionID: true}
	for id := info.ParentSession; id != "" && !seen[id]; {
		seen[id] = true
		parent, err := s.session(id)
		if err != nil {
			return "", err
		}
		if parent.RoomID != roomID {
			return "", errors.New("approval parent belongs to another room")
		}
		if parent.Closed.IsZero() && !parent.Closing.Active() {
			return parent.ID, nil
		}
		id = parent.ParentSession
	}
	return Owner, nil
}

func (s *Service) public(r Request) (Request, error) {
	at, err := s.holderName(r.atID)
	if err != nil {
		return Request{}, err
	}
	r.At = at
	return r, nil
}

func (s *Service) holderName(id string) (string, error) {
	if id == Owner {
		return Owner, nil
	}
	info, err := s.session(id)
	if err != nil {
		return "", err
	}
	return info.EffectiveName(), nil
}

func (s *Service) holderAddress(rec room.Record, id string) (string, map[string]string, error) {
	if id == Owner {
		return rec.Name, nil, nil
	}
	info, err := s.session(id)
	if err != nil {
		return "", nil, err
	}
	address := info.Address()
	return address, map[string]string{address: info.ID}, nil
}

func (s *Service) postRequested(r Request) error {
	rec, err := s.Rooms.Resolve(r.Room)
	if err != nil {
		return err
	}
	requester, err := s.session(r.Requester)
	if err != nil {
		return err
	}
	at, sessionIDs, err := s.holderAddress(rec, r.atID)
	if err != nil {
		return err
	}
	detail := fmt.Sprintf("Decide it with: dyna request approve %s\nRefuse it with: dyna request deny %s --reason \"<why>\"", r.ID, r.ID)
	ev := room.ApprovalRequestedEvent{RequestID: r.ID, Kind: string(r.Kind), Class: room.ApprovalClass(r.Class), Requester: requester.Address(), At: at, Repository: r.Repository, Server: r.Server, Command: r.Command, Tool: r.Tool, Title: r.Title, Body: r.Body, Detail: detail}
	_, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: r.Room, Event: ev, To: []string{at}, SessionIDs: sessionIDs, NoWake: r.atID == Owner})
	return err
}

func (s *Service) postDecided(r Request, actor string, action Action, reason, next string) error {
	rec, err := s.Rooms.Resolve(r.Room)
	if err != nil {
		return err
	}
	ev := room.ApprovalDecidedEvent{RequestID: r.ID, Actor: actor, Action: room.ApprovalAction(action), Reason: reason}
	in := room.PostEventInput{RoomID: r.Room, Event: ev, To: []string{rec.Name}, NoWake: true}
	if action == ActionPass {
		at, sessionIDs, err := s.holderAddress(rec, next)
		if err != nil {
			return err
		}
		ev.At = at
		in.To, in.SessionIDs, in.NoWake = []string{at}, sessionIDs, next == Owner
	}
	_, err = s.Rooms.PostEventInput(in)
	return err
}

func (s *Service) postResult(r Request, quiet bool) error {
	requester, err := s.session(r.Requester)
	if err != nil {
		return err
	}
	ev := room.ApprovalResultEvent{RequestID: r.ID, State: room.ApprovalState(r.State)}
	if r.Result != nil {
		ev.Actor, ev.Reason, ev.ExitCode, ev.URL, ev.Output = r.Result.Actor, r.Result.Reason, r.Result.ExitCode, r.Result.URL, r.Result.Output
	}
	address := requester.Address()
	_, err = s.Rooms.PostEventInput(room.PostEventInput{RoomID: r.Room, Event: ev, To: []string{address}, SessionIDs: map[string]string{address: requester.ID}, NoWake: quiet})
	return err
}

func (s *Service) changeChan(key string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changes == nil {
		s.changes = map[string]chan struct{}{}
	}
	ch := s.changes[key]
	if ch == nil {
		ch = make(chan struct{})
		s.changes[key] = ch
	}
	return ch
}

func (s *Service) changed(roomID, id string) {
	key := roomID + "\x00" + id
	s.mu.Lock()
	ch := s.changes[key]
	delete(s.changes, key)
	if ch != nil {
		close(ch)
	}
	s.mu.Unlock()
}
