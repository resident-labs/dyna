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

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// Boxes controls the lifetime of a session's box.
type Boxes interface {
	Start(context.Context, contract.SessionID, contract.BoxID, contract.BoxSizeName) (Placement, error)
	Close(context.Context, contract.BoxID) error
}

// Placement reports a refusal as a pending box and a reason.
type Placement struct {
	State  contract.BoxState
	Reason string
}

type Profiles interface {
	Model(contract.ProfileName) (contract.ModelChoice, []string, error)
}

type Service struct {
	store    *store.Store
	boxes    Boxes
	profiles Profiles
	now      func() time.Time
	mu       sync.Mutex
	channels map[contract.SessionID]*boxChannel
}

func New(st *store.Store, boxes Boxes, profiles Profiles, now func() time.Time) *Service {
	return &Service{store: st, boxes: boxes, profiles: profiles, now: now, channels: make(map[contract.SessionID]*boxChannel)}
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
		parent, err := s.store.Session(ctx, req.Parent)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		if parent.Room != req.Room {
			return contract.SessionStartResponse{}, forbidden()
		}
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
	if err := s.store.StartSession(ctx, session, req.Parent, req.Goal, event); err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	placement, err := s.boxes.Start(ctx, session.ID, session.Box, contract.BoxSizeSmall)
	if err != nil {
		placement = Placement{State: contract.BoxPending, Reason: err.Error()}
	}
	change, err := s.event(req.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, From: contract.BoxPending, To: placement.State, Reason: placement.Reason})
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	if err = s.store.SetBoxState(context.WithoutCancel(ctx), session.Box, placement.State, placement.Reason, change); err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	return contract.SessionStartResponse{Session: session.ID, Box: session.Box}, nil
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
	if err := s.boxes.Close(ctx, session.Box); err != nil {
		return contract.SessionCloseResponse{}, failure(err)
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
