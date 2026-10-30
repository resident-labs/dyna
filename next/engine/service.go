// Package engine implements the control-plane service.
package engine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type Profiles interface {
	Model(contract.ProfileName) (contract.ModelChoice, []string, error)
}

type Service struct {
	store     *store.Store
	boxes     BoxAdapter
	profiles  Profiles
	now       func() time.Time
	mu        sync.Mutex
	channels  map[contract.SessionID]*boxChannel
	admit     sync.Mutex
	boxLockMu sync.Mutex
	boxLocks  map[contract.SessionID]*sync.Mutex
	boxRuns   map[contract.SessionID]*boxRun
}

type boxRun struct {
	cancel  context.CancelFunc
	closing bool
}

func New(st *store.Store, boxes BoxAdapter, profiles Profiles, now func() time.Time) *Service {
	return &Service{store: st, boxes: boxes, profiles: profiles, now: now, channels: make(map[contract.SessionID]*boxChannel), boxLocks: make(map[contract.SessionID]*sync.Mutex), boxRuns: make(map[contract.SessionID]*boxRun)}
}

func (s *Service) lockBox(id contract.SessionID) func() {
	s.boxLockMu.Lock()
	lock := s.boxLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.boxLocks[id] = lock
	}
	s.boxLockMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func (s *Service) transition(ctx context.Context, room contract.RoomID, b box.Box, state contract.BoxState, reason string) (box.Box, error) {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{})
	if err != nil {
		return box.Box{}, err
	}
	return s.store.TransitionBox(ctx, b, state, reason, event)
}

func id(prefix string, now time.Time) (string, error) {
	var raw [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(raw[6:]); err != nil {
		return "", err
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	n := new(big.Int).SetBytes(raw[:])
	mask := big.NewInt(31)
	var result [26]byte
	for i := len(result) - 1; i >= 0; i-- {
		result[i] = alphabet[new(big.Int).And(n, mask).Int64()]
		n.Rsh(n, 5)
	}
	return prefix + string(result[:]), nil
}

func failure(err error) *contract.Error {
	code := contract.ErrUnavailable
	if errors.Is(err, sql.ErrNoRows) {
		code = contract.ErrNotFound
	} else if errors.Is(err, store.ErrTurnConflict) {
		code = contract.ErrConflict
	}
	return &contract.Error{Code: code, Message: err.Error()}
}
func invalid(message string) *contract.Error {
	return &contract.Error{Code: contract.ErrInvalid, Message: message}
}
func forbidden() *contract.Error {
	return &contract.Error{Code: contract.ErrForbidden, Message: "caller cannot access this resource"}
}

func (s *Service) event(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
	now := s.now()
	value, err := id("ev_", now)
	return contract.Event{ID: contract.EventID(value), Time: now, Room: room, From: from, Body: body}, err
}

func (s *Service) roomAccess(ctx context.Context, caller contract.Principal, room contract.RoomID) *contract.Error {
	if caller.Kind == contract.PrincipalOwner {
		return nil
	}
	if caller.Kind != contract.PrincipalSession {
		return forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return failure(err)
	}
	if session.Room != room {
		return forbidden()
	}
	return nil
}

func (s *Service) RoomCreate(ctx context.Context, caller contract.Principal, req contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomCreateResponse{}, forbidden()
	}
	if strings.TrimSpace(req.Name) == "" {
		return contract.RoomCreateResponse{}, invalid("room name is required")
	}
	now := s.now()
	value, err := id("room_", now)
	if err != nil {
		return contract.RoomCreateResponse{}, failure(err)
	}
	room := contract.RoomID(value)
	if err := s.store.CreateRoom(ctx, room, req.Name, now); err != nil {
		return contract.RoomCreateResponse{}, failure(err)
	}
	return contract.RoomCreateResponse{Room: room}, nil
}

func (s *Service) SessionStart(ctx context.Context, caller contract.Principal, req contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.SessionStartResponse{}, forbidden()
	}
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.SessionStartResponse{}, e
	}
	if req.Name == "" || req.Profile == "" {
		return contract.SessionStartResponse{}, invalid("name and profile are required")
	}
	if _, _, err := s.profiles.Model(req.Profile); err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	if req.Parent != "" {
		return contract.SessionStartResponse{}, invalid("child sessions are not supported in M1")
	}
	now := s.now()
	sid, err := id("ses_", now)
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	bid, err := id("box_", now)
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	session := store.Session{ID: contract.SessionID(sid), Room: req.Room, Name: req.Name, Profile: req.Profile, Box: contract.BoxID(bid)}
	event, err := s.event(req.Room, caller, contract.SessionStarted{Session: session.ID, Name: req.Name, Parent: req.Parent, Profile: req.Profile, Goal: req.Goal, Brief: req.Brief})
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	unlock := s.lockBox(session.ID)
	defer unlock()
	createCtx, cancel := context.WithCancel(ctx)
	run := &boxRun{cancel: cancel}
	s.boxLockMu.Lock()
	s.boxRuns[session.ID] = run
	s.boxLockMu.Unlock()
	defer func() {
		cancel()
		s.boxLockMu.Lock()
		delete(s.boxRuns, session.ID)
		s.boxLockMu.Unlock()
	}()
	response := contract.SessionStartResponse{Session: session.ID, Box: session.Box}
	s.admit.Lock()
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	size, _ := box.Size(contract.BoxSizeSmall)
	current := box.Box{ID: session.Box, Session: session.ID, Size: size, State: contract.BoxPending, PlacedAt: event.Time}
	placement := box.Admit(hosts, boxes, current, 20*box.GiB)
	initial, err := s.event(req.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending, Reason: string(placement.Reason)})
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	if err = s.store.StartSession(ctx, session, req.Parent, req.Goal, event, initial, string(placement.Reason)); err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	writeCtx := context.WithoutCancel(ctx)
	if placement.Reason != "" {
		s.admit.Unlock()
		return response, nil
	}
	slice, err := box.SliceName([]contract.BoxID{session.Box})
	if err != nil {
		s.admit.Unlock()
		return response, failure(err)
	}
	current.Host, current.Slice = placement.Host, slice
	current, err = s.transition(writeCtx, session.Room, current, contract.BoxCreating, "")
	s.admit.Unlock()
	if err != nil {
		return response, failure(err)
	}
	image, createErr := s.boxes.Create(createCtx, BoxSpec{Box: current.ID, Session: current.Session, Host: current.Host, Slice: current.Slice, Size: current.Size})
	if createErr != nil {
		s.boxLockMu.Lock()
		closing := run.closing
		s.boxLockMu.Unlock()
		if closing {
			return response, nil
		}
		_, err = s.transition(writeCtx, session.Room, current, contract.BoxFailed, createErr.Error())
		if err != nil {
			return response, failure(err)
		}
		return response, nil
	}
	current.Image = image
	if _, err = s.transition(writeCtx, session.Room, current, contract.BoxStarting, ""); err != nil {
		return response, failure(err)
	}
	return response, nil
}

func (s *Service) SessionClose(ctx context.Context, caller contract.Principal, req contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.SessionCloseResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, req.Session)
	if err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if e := s.roomAccess(ctx, caller, session.Room); e != nil {
		return contract.SessionCloseResponse{}, e
	}
	s.boxLockMu.Lock()
	if run := s.boxRuns[session.ID]; run != nil {
		run.closing = true
		run.cancel()
	}
	s.boxLockMu.Unlock()
	unlock := s.lockBox(session.ID)
	defer unlock()
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if current.State != contract.BoxDeleted {
		if current.State != contract.BoxClosing {
			current, err = s.transition(ctx, session.Room, current, contract.BoxClosing, "")
			if err != nil {
				return contract.SessionCloseResponse{}, failure(err)
			}
		}
		if err = s.boxes.Remove(ctx, session.Box); err != nil {
			_, transitionErr := s.transition(context.WithoutCancel(ctx), session.Room, current, contract.BoxFailed, err.Error())
			if transitionErr != nil {
				return contract.SessionCloseResponse{}, failure(transitionErr)
			}
			return contract.SessionCloseResponse{}, failure(err)
		}
		if _, err = s.transition(context.WithoutCancel(ctx), session.Room, current, contract.BoxDeleted, ""); err != nil {
			return contract.SessionCloseResponse{}, failure(err)
		}
	}
	event, err := s.event(session.Room, caller, contract.SessionClosed{Session: session.ID})
	if err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if err = s.store.CloseSession(ctx, session.ID, event); err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	return contract.SessionCloseResponse{}, nil
}

func (s *Service) Post(ctx context.Context, caller contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.PostResponse{}, e
	}
	if strings.TrimSpace(req.Text) == "" {
		return contract.PostResponse{}, invalid("post text is required")
	}
	var mentions []contract.Principal
	var recipients []contract.SessionID
	seen := make(map[contract.SessionID]bool)
	for _, name := range req.Mentions {
		session, err := s.store.SessionByName(ctx, req.Room, strings.TrimPrefix(name, "@"))
		if err != nil {
			return contract.PostResponse{}, failure(err)
		}
		if seen[session.ID] {
			continue
		}
		seen[session.ID] = true
		mentions = append(mentions, contract.Principal{Kind: contract.PrincipalSession, Session: session.ID})
		if caller.Kind != contract.PrincipalSession || caller.Session != session.ID {
			recipients = append(recipients, session.ID)
		}
	}
	event, err := s.event(req.Room, caller, contract.PostCreated{Text: req.Text, Mentions: mentions, Home: contract.Conversation{Kind: contract.ConversationFeed}})
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	seq, err := s.store.Post(ctx, event, recipients)
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	return contract.PostResponse{Event: event.ID, Seq: seq}, nil
}

func (s *Service) EventsList(ctx context.Context, caller contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.EventsListResponse{}, e
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit < 0 || req.AfterSeq < 0 {
		return contract.EventsListResponse{}, invalid("invalid event range")
	}
	events, err := s.store.Events(ctx, req.Room, req.AfterSeq, req.Limit, req.Types)
	if err != nil {
		return contract.EventsListResponse{}, failure(err)
	}
	next := req.AfterSeq
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	return contract.EventsListResponse{Events: events, Next: next}, nil
}

func (s *Service) BoxHello(ctx context.Context, caller contract.Principal, req contract.BoxHelloRequest) (contract.BoxHelloResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.BoxHelloResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	unlock := s.lockBox(session.ID)
	defer unlock()
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	if current.State == contract.BoxStarting {
		if _, err = s.transition(ctx, session.Room, current, contract.BoxReady, ""); err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	} else if current.State != contract.BoxReady {
		return contract.BoxHelloResponse{}, invalid("box is not starting or ready")
	}
	model, _, err := s.profiles.Model(session.Profile)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	return contract.BoxHelloResponse{Session: session.ID, Box: session.Box, Model: model}, nil
}

func (s *Service) DeliveryDone(ctx context.Context, caller contract.Principal, req contract.DeliveryDoneRequest) (contract.DeliveryDoneResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.DeliveryDoneResponse{}, forbidden()
	}
	if err := s.store.DeliveryDone(ctx, caller.Session, req.Delivery); err != nil {
		return contract.DeliveryDoneResponse{}, failure(err)
	}
	return contract.DeliveryDoneResponse{}, nil
}

func (s *Service) TurnOutput(ctx context.Context, caller contract.Principal, req contract.TurnOutputRequest) (contract.TurnOutputResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TurnOutputResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if req.Outcome != contract.TurnCompleted && req.Outcome != contract.TurnFailed && req.Outcome != contract.TurnStopped {
		return contract.TurnOutputResponse{}, invalid("invalid turn outcome")
	}
	req.Usage.Model = req.Model
	event, err := s.event(session.Room, caller, contract.TurnEnded{Session: session.ID, Turn: req.Turn, Outcome: req.Outcome, Final: req.Final, Usage: req.Usage, Error: req.Error})
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if err = s.store.EndTurn(ctx, session.ID, req, event); err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	return contract.TurnOutputResponse{}, nil
}
