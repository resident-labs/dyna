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
	gitName, gitEmail  string
	maxDepth           int
	store              *store.Store
	dispatch           func(context.Context, contract.SessionID, contract.TurnID, contract.Event, bool) (contract.TurnID, []store.FeedInput, error)
	boxes              lifecycle.BoxAdapter
	reconciler         *lifecycle.Reconciler
	profiles           Profiles
	repos              Repositories
	approvals          *approve.Service
	hostexec           HostexecRunner
	hostexecTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	now                func() time.Time
	mu                 sync.Mutex
	live               *live.Live
	sessions           map[contract.SessionID]*sessionState
	computerGeneration uint64
	shellGeneration    uint64
	readMu             sync.Mutex // preserves commit and live broadcast order
	// transcriptMu serializes appends, so each offset check and write runs alone.
	transcriptMu        sync.Mutex
	transcriptDir       string
	filesDir            string
	instructionsMu      sync.Mutex
	publishInstructions func(context.Context)
}

// sessionState is the engine's in-memory state for one session. s.mu guards its fields.
// computerLock serializes computer transitions, channel replacement and watcher delivery.
// Take computerLock before s.mu, never the reverse.
type sessionState struct {
	computerLock sync.Mutex
	channel      *boxChannel
	computer     *computerState
	terminals    int
	watch        *shellWatch
}

// sessionLocked creates the entry if needed. Callers hold s.mu and pass the id of a session that exists or is being started.
func (s *Service) sessionLocked(id contract.SessionID) *sessionState {
	state := s.sessions[id]
	if state == nil {
		state = &sessionState{}
		s.sessions[id] = state
	}
	return state
}

// lockSession returns the session with its computer lock held.
func (s *Service) lockSession(id contract.SessionID) *sessionState {
	s.mu.Lock()
	state := s.sessionLocked(id)
	s.mu.Unlock()
	state.computerLock.Lock()
	return state
}

func New(st *store.Store, boxes lifecycle.BoxAdapter, profiles Profiles, now func() time.Time) *Service {
	s := &Service{sessions: make(map[contract.SessionID]*sessionState), hostexecTimeout: context.WithTimeout, store: st, boxes: boxes, profiles: profiles, now: now}
	s.reconciler = lifecycle.New(st, boxes, s, nil, s.event, now)
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

func (s *Service) OpenTerminal(id contract.SessionID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id] != nil && s.sessions[id].terminals > 0
}

// SetTranscriptDir sets where transcript copies are kept, one folder per session.
func (s *Service) SetTranscriptDir(dir string) { s.transcriptDir = dir }

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
