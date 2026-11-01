// Package engine implements the control-plane service.
package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/live"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

type Profiles interface {
	List() []contract.ProfileSummary
	CompactionTokens(contract.ProfileName) int
	Model(contract.ProfileName) (contract.ModelChoice, []string, error)
}

// Repositories reads room repositories and prepares their host caches.
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
	Ref(context.Context, contract.RepoName, string) (string, error)
	DeleteArchiveRef(context.Context, contract.RepoName, string, string) error
	Size(context.Context, contract.RepoName) (contract.BoxSizeName, error)
}

type Service struct {
	// profileMoved runs after sessions change model, so their account choices can follow.
	profileMoved      func(context.Context, []store.Session, contract.ProfileName)
	gitName, gitEmail string
	maxDepth          int
	store             *store.Store
	searchBraveURL    string
	searchProxy       *model.Proxy
	searchQuota       *model.Quotas
	searchMu          sync.Mutex
	searchFailures    map[string]time.Time
	dispatch          func(context.Context, contract.SessionID, contract.TurnID, contract.Event, bool) (contract.TurnID, []store.FeedInput, error)
	boxes             lifecycle.BoxAdapter
	reconciler        *lifecycle.Reconciler
	profiles          Profiles
	repos             Repositories
	approvals         *approve.Service
	hostexec          HostexecRunner
	hostexecTimeout   func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	now               func() time.Time
	live              *live.Live
	readMu            sync.Mutex // preserves commit and live broadcast order
	// transcriptMu serializes appends, so each offset check and write runs alone.
	transcriptMu        sync.Mutex
	layout              box.Layout
	roomData            sync.RWMutex // writers of a room folder share it; removing one takes it alone
	instructionsMu      sync.Mutex
	publishInstructions func(context.Context)
}

// createSession publishes before checking the store, so close can retire the handle.
func (s *Service) createSession(ctx context.Context, id contract.SessionID) (*live.Session, *contract.Error) {
	handle, _ := s.live.CreateSession(id)
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_, open, err := s.store.OpenSession(check, id)
	cancel()
	if err != nil {
		// A failed store read after close can leave an empty handle.
		return nil, failure(err)
	}
	if !open {
		s.retireSession(s.live.RetireHandle(handle), id)
	}
	if err := ctx.Err(); err != nil {
		return nil, failure(err)
	}
	if !open {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	return handle, nil
}

func (s *Service) lockSession(ctx context.Context, id contract.SessionID) (*live.Session, *contract.Error) {
	handle, problem := s.createSession(ctx, id)
	if problem != nil {
		return nil, problem
	}
	if !handle.Lock() {
		return nil, &contract.Error{Code: contract.ErrNotFound, Message: "session is closed"}
	}
	return handle, nil
}

// lookupSession reads existing session state without creating it.
func (s *Service) lookupSession(id contract.SessionID) (*live.Session, bool) {
	handle, ok := s.live.LookupSession(id)
	if !ok || !handle.Lock() {
		return nil, false
	}
	return handle, true
}

func New(st *store.Store, boxes lifecycle.BoxAdapter, profiles Profiles, now func() time.Time) *Service {
	s := &Service{hostexecTimeout: context.WithTimeout, store: st, boxes: boxes, profiles: profiles, now: now}
	s.reconciler = lifecycle.New(st, boxes, s, nil, s.event, now)
	s.reconciler.SetRoomData(s)
	s.live = live.New(s.deliverWakes)
	st.SetWake(s.queueWakes)
	go s.live.Run(st.Done())
	return s
}

// SetGitIdentity configures the commit identity sent to boxes.
func (s *Service) SetGitIdentity(name, email string) { s.gitName, s.gitEmail = name, email }

// SetMaxDepth sets the session depth limit before the service starts; zero disables it.
func (s *Service) SetMaxDepth(depth int) { s.maxDepth = depth }

// SetRepositories configures repository lookup before the service starts.
func (s *Service) SetRepositories(repos Repositories) {
	s.repos = repos
	s.reconciler.SetRepositories(repos)
}

func (s *Service) Reconciler() *lifecycle.Reconciler { return s.reconciler }

// SessionClosed retires live session state after the close commits.
func (s *Service) SessionClosed(id contract.SessionID) {
	s.retireSession(s.live.RetireSession(id), id)
}

func (s *Service) retireSession(handle *live.Session, id contract.SessionID) {
	if handle == nil {
		return
	}
	defer handle.Unlock()
	channel := handle.Channel()
	handle.SetChannel(nil)
	room, stopped := handle.ClearComputer()
	handle.ClearShell()
	channel.Close()
	if stopped {
		_ = s.computerEvent(context.Background(), id, room, contract.ComputerStopped)
	}
}

func (s *Service) OpenTerminal(id contract.SessionID) bool {
	handle, ok := s.live.LookupSession(id)
	return ok && handle.HasTerminal()
}

// SetLayout sets where room data is kept, before the service starts.
func (s *Service) SetLayout(layout box.Layout) { s.layout = layout }

func (s *Service) transition(ctx context.Context, room contract.RoomID, b box.Box, state contract.BoxState, reason string, cause contract.FailureCause) (box.Box, error) {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Cause: cause})
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
	if errors.Is(err, approve.ErrForbidden) {
		code = contract.ErrForbidden
	} else if errors.Is(err, approve.ErrInvalid) {
		code = contract.ErrInvalid
	} else if errors.Is(err, approve.ErrConflict) {
		code = contract.ErrConflict
	} else if errors.Is(err, store.ErrNotFound) {
		code = contract.ErrNotFound
	} else if errors.Is(err, store.ErrSessionClosing) {
		code = contract.ErrSessionClosing
	} else if errors.Is(err, store.ErrParentClosing) {
		code = contract.ErrParentClosing
	} else if errors.Is(err, store.ErrTurnConflict) || errors.Is(err, store.ErrRoomDeleting) {
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
