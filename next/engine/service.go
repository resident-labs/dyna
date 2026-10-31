// Package engine implements the control-plane service.
package engine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

type Profiles interface {
	Model(contract.ProfileName) (contract.ModelChoice, []string, error)
}

// Repositories reads room repositories and prepares their host caches.
type Repositories interface {
	Get(context.Context, contract.SessionID, contract.HostName, bool) ([]contract.BoxRepo, error)
	Ref(context.Context, contract.RepoName, string) (string, error)
}

type Service struct {
	store              *store.Store
	boxes              BoxAdapter
	profiles           Profiles
	repos              Repositories
	approvals          *approve.Service
	hostexec           HostexecRunner
	hostexecTimeout    func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	now                func() time.Time
	mu                 sync.Mutex
	channels           map[contract.SessionID]*boxChannel
	activity           map[contract.SessionID]contract.TurnActivity
	live               map[chan contract.Event]contract.RoomID
	computers          map[contract.SessionID]*computerState
	terminals          map[contract.SessionID]int
	computerGeneration uint64
	shellGeneration    uint64
	shellWatches       map[contract.SessionID]*shellWatch
	admit              sync.Mutex
	boxLockMu          sync.Mutex
	boxLocks           map[contract.SessionID]*sync.Mutex
	boxRuns            map[contract.SessionID]*boxRun
	moveMu             sync.Mutex
	moveStops          []contract.BoxID
	dormancy           dormancy
	// transcriptMu serializes appends, so each offset check and write runs alone.
	transcriptMu  sync.Mutex
	transcriptDir string
	filesDir      string
}

type boxRun struct {
	cancel  context.CancelFunc
	closing bool
}

func New(st *store.Store, boxes BoxAdapter, profiles Profiles, now func() time.Time) *Service {
	return &Service{terminals: make(map[contract.SessionID]int), activity: make(map[contract.SessionID]contract.TurnActivity), live: make(map[chan contract.Event]contract.RoomID), hostexecTimeout: context.WithTimeout, store: st, boxes: boxes, profiles: profiles, now: now, channels: make(map[contract.SessionID]*boxChannel), computers: make(map[contract.SessionID]*computerState), shellWatches: make(map[contract.SessionID]*shellWatch), boxLocks: make(map[contract.SessionID]*sync.Mutex), boxRuns: make(map[contract.SessionID]*boxRun), dormancy: dormancy{unconfirmed: make(map[contract.BoxID]bool), failedStarts: make(map[contract.BoxID]bool)}}
}

// SetRepositories configures repository lookup before the service starts.
func (s *Service) SetRepositories(repos Repositories) { s.repos = repos }

// SetTranscriptDir sets where transcript copies are kept, one folder per session.
func (s *Service) SetTranscriptDir(dir string) { s.transcriptDir = dir }

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
	} else if errors.Is(err, sql.ErrNoRows) {
		code = contract.ErrNotFound
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

var mentionName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *Service) RoomCreate(ctx context.Context, caller contract.Principal, req contract.RoomCreateRequest) (contract.RoomCreateResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomCreateResponse{}, forbidden()
	}
	if !mentionName.MatchString(req.Name) {
		return contract.RoomCreateResponse{}, invalid("room name must be 1–64 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	if req.RootProfile == "" {
		return contract.RoomCreateResponse{}, invalid("room name and root profile are required")
	}
	now := s.now()
	value, err := id("room_", now)
	if err != nil {
		return contract.RoomCreateResponse{}, failure(err)
	}
	room := contract.RoomID(value)
	if _, e := s.startSession(ctx, caller, contract.SessionStartRequest{Room: room, Name: "root", Profile: req.RootProfile}, req.Name); e != nil {
		return contract.RoomCreateResponse{}, e
	}
	return contract.RoomCreateResponse{Room: room}, nil
}

func (s *Service) RoomRepoAdd(ctx context.Context, caller contract.Principal, req contract.RoomRepoAddRequest) (contract.RoomRepoAddResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomRepoAddResponse{}, forbidden()
	}
	owner, name, ok := strings.Cut(string(req.Repo), "/")
	ownerOK, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`, owner)
	nameOK, _ := regexp.MatchString(`^[A-Za-z0-9_.-]+$`, name)
	if !ok || !ownerOK || !nameOK || name == "." || name == ".." {
		return contract.RoomRepoAddResponse{}, invalid("repository must be owner/name")
	}
	if err := s.store.AddRoomRepo(ctx, req.Room, req.Repo, req.Default); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return contract.RoomRepoAddResponse{}, invalid("unknown room")
		}
		return contract.RoomRepoAddResponse{}, failure(err)
	}
	return contract.RoomRepoAddResponse{}, nil
}

func (s *Service) RoomRepos(ctx context.Context, caller contract.Principal, req contract.RoomReposRequest) (contract.RoomReposResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.RoomReposResponse{}, forbidden()
	}
	repos, err := s.store.RoomRepos(ctx, req.Room)
	if errors.Is(err, sql.ErrNoRows) {
		return contract.RoomReposResponse{}, invalid("unknown room")
	}
	if err != nil {
		return contract.RoomReposResponse{}, failure(err)
	}
	return contract.RoomReposResponse{Repos: repos}, nil
}

func (s *Service) SessionStart(ctx context.Context, caller contract.Principal, req contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
	if !mentionName.MatchString(req.Name) {
		return contract.SessionStartResponse{}, invalid("session name must be 1–64 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	return s.startSession(ctx, caller, req, "")
}

func (s *Service) startSession(ctx context.Context, caller contract.Principal, req contract.SessionStartRequest, roomName string) (contract.SessionStartResponse, *contract.Error) {
	if roomName == "" {
		if e := s.roomAccess(ctx, caller, req.Room); e != nil {
			return contract.SessionStartResponse{}, e
		}
	}
	if req.Name == "" || req.Profile == "" {
		return contract.SessionStartResponse{}, invalid("name and profile are required")
	}
	if _, _, err := s.profiles.Model(req.Profile); err != nil {
		if roomName != "" {
			return contract.SessionStartResponse{}, invalid("unknown profile")
		}
		return contract.SessionStartResponse{}, failure(err)
	}
	tree, err := s.store.RoomTree(ctx, req.Room)
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	parent := contract.SessionID("")
	if roomName == "" {
		parent, err = tree.ParentFor(caller, req.Parent)
		if err != nil {
			return contract.SessionStartResponse{}, invalid(err.Error())
		}
	}
	var repo contract.RepoName
	if roomName == "" {
		repos, err := s.store.RoomRepos(ctx, req.Room)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		p, err := s.store.Session(ctx, parent)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		repo, err = room.RepoForSession(repos, req.Repo, p.Repo)
		if err != nil {
			return contract.SessionStartResponse{}, invalid(err.Error())
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
	session := store.Session{ID: contract.SessionID(sid), Room: req.Room, Name: req.Name, Repo: repo, Profile: req.Profile, Box: contract.BoxID(bid), NewRoomName: roomName}
	event, err := s.event(req.Room, caller, contract.SessionStarted{Session: session.ID, Name: req.Name, Parent: parent, Profile: req.Profile, Goal: req.Goal, Brief: req.Brief})
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
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	size, _ := box.Size(contract.BoxSizeSmall)
	current := box.Box{ID: session.Box, Session: session.ID, Size: size, State: contract.BoxPending, PlacedAt: event.Time}
	var ancestry []contract.BoxID
	if parent != "" && parent != tree.Root() {
		p, err := s.store.Session(ctx, parent)
		if err != nil {
			s.admit.Unlock()
			return contract.SessionStartResponse{}, failure(err)
		}
		current.BudgetParent = p.Box
		for _, b := range boxes {
			if b.ID == p.Box && (b.State == contract.BoxClosing || b.State == contract.BoxDeleted) {
				s.admit.Unlock()
				return contract.SessionStartResponse{}, invalid("parent box is closing")
			}
		}
		path, err := s.store.BoxAncestry(ctx, p.Box)
		if err != nil {
			s.admit.Unlock()
			return contract.SessionStartResponse{}, failure(err)
		}
		for _, b := range path {
			ancestry = append(ancestry, b.ID)
		}
	}
	placement, err := s.place(ctx, current, boxes)
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	initial, err := s.event(req.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending, Reason: string(placement.Reason)})
	if err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	if err = s.store.StartSession(ctx, session, parent, current.BudgetParent, req.Goal, event, initial, string(placement.Reason)); err != nil {
		s.admit.Unlock()
		return contract.SessionStartResponse{}, failure(err)
	}
	writeCtx := context.WithoutCancel(ctx)
	if placement.Reason != "" {
		s.admit.Unlock()
		return response, nil
	}
	slice, err := box.SliceName(append(ancestry, session.Box))
	if err != nil {
		s.admit.Unlock()
		return response, failure(err)
	}
	current.Host, current.Slice = placement.Host, slice
	current, err = s.transition(writeCtx, session.Room, current, contract.BoxCreating, "", "")
	s.admit.Unlock()
	if err != nil {
		return response, failure(err)
	}
	if err := s.createPlaced(createCtx, writeCtx, session.Room, current, run); err != nil {
		return response, failure(err)
	}
	return response, nil
}

// createPlaced completes a reserved box and wakes children waiting for its host.
func (s *Service) createPlaced(ctx, writeCtx context.Context, room contract.RoomID, current box.Box, run *boxRun) error {
	var repos []contract.BoxRepo
	var createErr error
	if s.repos != nil {
		repos, createErr = s.repos.Get(ctx, current.Session, current.Host, true)
	}
	var image string
	if createErr == nil {
		image, createErr = s.boxes.Create(ctx, BoxSpec{Box: current.ID, Session: current.Session, Host: current.Host, Slice: current.Slice, Size: current.Size, Repos: repos})
	}
	if createErr != nil {
		s.boxLockMu.Lock()
		closing := run.closing
		s.boxLockMu.Unlock()
		if closing {
			return nil
		}
		_, err := s.transition(writeCtx, room, current, contract.BoxFailed, createErr.Error(), contract.CauseCreateFailed)
		return err
	}
	current.Image = image
	s.admit.Lock()
	s.boxLockMu.Lock()
	closing := run.closing
	s.boxLockMu.Unlock()
	if closing {
		s.admit.Unlock()
		return nil
	}
	_, err := s.transition(writeCtx, room, current, contract.BoxStarting, "", "")
	s.admit.Unlock()
	if err != nil {
		return err
	}
	return s.retryChildren(writeCtx, current.ID)
}

func (s *Service) place(ctx context.Context, current box.Box, boxes []box.Box) (box.Placement, error) {
	hosts, err := s.store.Hosts(ctx)
	if err != nil {
		return box.Placement{}, err
	}
	return box.Admit(hosts, boxes, current, 20*box.GiB), nil
}

func (s *Service) retryChildren(ctx context.Context, parent contract.BoxID) error {
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return err
	}
	for _, child := range boxes {
		if child.BudgetParent != parent || child.State != contract.BoxPending || child.Reason != string(box.PendingParentHost) {
			continue
		}
		go func(id contract.SessionID) { _ = s.retryChild(ctx, id) }(child.Session)
	}
	return nil
}

func (s *Service) retryChild(ctx context.Context, id contract.SessionID) error {
	unlock := s.lockBox(id)
	defer unlock()
	s.admit.Lock()
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		s.admit.Unlock()
		return err
	}
	var current box.Box
	for _, b := range boxes {
		if b.Session == id {
			current = b
			break
		}
	}
	if current.State != contract.BoxPending || current.Reason != string(box.PendingParentHost) {
		s.admit.Unlock()
		return nil
	}
	placement, err := s.place(ctx, current, boxes)
	if err != nil {
		s.admit.Unlock()
		return err
	}
	if placement.Reason != "" {
		s.admit.Unlock()
		return nil
	}
	path, err := s.store.BoxAncestry(ctx, current.ID)
	if err != nil {
		s.admit.Unlock()
		return err
	}
	var ancestry []contract.BoxID
	for _, b := range path {
		ancestry = append(ancestry, b.ID)
	}
	current.Slice, err = box.SliceName(ancestry)
	if err != nil {
		s.admit.Unlock()
		return err
	}
	current.Host = placement.Host
	session, err := s.store.Session(ctx, current.Session)
	if err != nil {
		s.admit.Unlock()
		return err
	}
	createCtx, cancel := context.WithCancel(ctx)
	run := &boxRun{cancel: cancel}
	s.boxLockMu.Lock()
	s.boxRuns[id] = run
	s.boxLockMu.Unlock()
	defer func() { cancel(); s.boxLockMu.Lock(); delete(s.boxRuns, id); s.boxLockMu.Unlock() }()
	current, err = s.transition(ctx, session.Room, current, contract.BoxCreating, "", "")
	s.admit.Unlock()
	if err != nil {
		return err
	}
	return s.createPlaced(createCtx, ctx, session.Room, current, run)

}

func (s *Service) closeGuard(ctx context.Context, parent contract.BoxID) *contract.Error {
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return failure(err)
	}
	for _, b := range boxes {
		if b.BudgetParent == parent && b.State != contract.BoxDeleted {
			return &contract.Error{Code: contract.ErrHasChildren, Message: "box has child boxes"}
		}
	}
	return nil
}

func (s *Service) SessionClose(ctx context.Context, caller contract.Principal, req contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error) {
	if req.Discard && caller.Kind != contract.PrincipalOwner {
		return contract.SessionCloseResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, req.Session)
	if err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if e := s.roomAccess(ctx, caller, session.Room); e != nil {
		return contract.SessionCloseResponse{}, e
	}
	var tree room.Tree
	var order []contract.SessionID
	if e := func() *contract.Error {
		s.admit.Lock()
		defer s.admit.Unlock()
		tree, err = s.store.RoomTree(ctx, session.Room)
		if err != nil {
			return failure(err)
		}
		if !tree.CanManage(caller, session.ID) {
			return forbidden()
		}
		order, err = tree.CloseOrder(session.ID)
		if err != nil {
			return &contract.Error{Code: contract.ErrForbidden, Message: err.Error()}
		}
		s.boxLockMu.Lock()
		for _, id := range order {
			if id != session.ID && s.boxRuns[id] != nil {
				s.boxLockMu.Unlock()
				return &contract.Error{Code: contract.ErrConflict, Message: "child is starting"}
			}
		}
		s.boxLockMu.Unlock()
		if err := s.store.MarkClosing(ctx, session.ID); err != nil {
			return failure(err)
		}
		return nil
	}(); e != nil {
		return contract.SessionCloseResponse{}, e
	}
	for _, id := range tree.Leaves(order) {
		child, err := s.store.Session(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return contract.SessionCloseResponse{}, failure(err)
		}
		if e := s.closeSession(ctx, caller, child, req.Discard); e != nil {
			return contract.SessionCloseResponse{}, e
		}
	}
	return contract.SessionCloseResponse{}, nil
}

func (s *Service) closeSession(ctx context.Context, caller contract.Principal, session store.Session, discard bool) *contract.Error {
	s.admit.Lock()
	if e := s.closeGuard(ctx, session.Box); e != nil {
		s.admit.Unlock()
		return e
	}
	s.boxLockMu.Lock()
	if run := s.boxRuns[session.ID]; run != nil {
		run.closing = true
		run.cancel()
	}
	s.boxLockMu.Unlock()
	s.admit.Unlock()
	unlock := s.lockBox(session.ID)
	defer unlock()
	s.admit.Lock()
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		s.admit.Unlock()
		return failure(err)
	}
	hasTurns, err := s.store.SessionHasTurns(ctx, session.ID)
	if err == nil && current.State != contract.BoxClosing && current.State != contract.BoxDeleted {
		current, err = s.transition(ctx, session.Room, current, contract.BoxClosing, "", "")
	}
	s.admit.Unlock()
	if err != nil {
		return failure(err)
	}
	if _, err := s.store.Session(ctx, session.ID); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return failure(err)
	}
	current, err = s.store.Box(ctx, session.Box)
	if err != nil {
		return failure(err)
	}
	if !discard && hasTurns && current.State != contract.BoxDeleted {
		if err := s.boxes.Start(ctx, current.ID); err != nil {
			return failure(err)
		}
		if current.Reason != "" {
			if err := s.archiveReason(ctx, session.Room, current, ""); err != nil {
				return failure(err)
			}
		}
		return nil
	}
	return s.finishClose(ctx, session, current, caller, discard)
}

func (s *Service) finishClose(ctx context.Context, session store.Session, current box.Box, caller contract.Principal, discard bool) *contract.Error {
	if current.State != contract.BoxDeleted {
		if err := s.boxes.Remove(ctx, session.Box); err != nil {
			_, transitionErr := s.transition(context.WithoutCancel(ctx), session.Room, current, contract.BoxFailed, err.Error(), contract.CauseRemoveFailed)
			if transitionErr != nil {
				return failure(transitionErr)
			}
			return failure(err)
		}
		if _, err := s.transition(context.WithoutCancel(ctx), session.Room, current, contract.BoxDeleted, "", ""); err != nil {
			return failure(err)
		}
	}
	event, err := s.event(session.Room, caller, contract.SessionClosed{Session: session.ID})
	if err != nil {
		return failure(err)
	}
	if err := s.store.CloseSession(ctx, session.ID, event); err != nil {
		return failure(err)
	}
	parent, err := s.store.ClosingParent(ctx, session.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return failure(err)
	}
	_, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: parent, Discard: discard})
	return e
}

func (s *Service) Post(ctx context.Context, caller contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.PostResponse{}, e
	}
	if strings.TrimSpace(req.Text) == "" {
		return contract.PostResponse{}, invalid("post text is required")
	}
	if len(req.Attachments) > 16 {
		return contract.PostResponse{}, invalid("too many attachments")
	}
	for i := range req.Attachments {
		file := &req.Attachments[i]
		media, _, mediaErr := mime.ParseMediaType(file.Media)
		if !validHash(file.SHA256) || file.Name == "" || len(file.Name) > 255 || mediaErr != nil {
			return contract.PostResponse{}, invalid("invalid attachment")
		}
		info, err := os.Stat(filepath.Join(s.filesDir, file.SHA256))
		if err != nil || !info.Mode().IsRegular() {
			return contract.PostResponse{}, invalid("attachment not found")
		}
		file.Bytes = info.Size()
		file.Media = media
	}
	if req.Home.Kind == "" {
		req.Home.Kind = contract.ConversationFeed
	}
	surface := contract.SurfaceFeed
	if req.Home.Kind == contract.ConversationDM {
		if caller.Kind != contract.PrincipalOwner || req.Home.Session == "" || len(req.Mentions) != 0 {
			return contract.PostResponse{}, invalid("invalid DM")
		}
		session, err := s.store.Session(ctx, req.Home.Session)
		if err != nil {
			return contract.PostResponse{}, failure(err)
		}
		if session.Room != req.Room {
			return contract.PostResponse{}, invalid("DM session is in another room")
		}
		surface = contract.SurfaceDM
	} else if req.Home.Kind != contract.ConversationFeed || req.Home.Session != "" {
		return contract.PostResponse{}, invalid("invalid conversation")
	}
	tree, err := s.store.RoomTree(ctx, req.Room)
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	var mentions []contract.Principal
	var recipients []room.Recipient
	seen := make(map[contract.SessionID]bool)
	for _, name := range req.Mentions {
		if !strings.HasPrefix(name, "@") {
			name = "@" + name
		}
		address, err := room.ParseAddress(name)
		if err != nil {
			return contract.PostResponse{}, invalid(err.Error())
		}
		names := make(map[string]contract.SessionID)
		if address.Kind == room.AddressName {
			targetRoom := req.Room
			if address.Room != "" {
				targetRoom, err = s.store.RoomByName(ctx, address.Room)
				if errors.Is(err, sql.ErrNoRows) {
					return contract.PostResponse{}, invalid("unknown room")
				}
				if err != nil {
					return contract.PostResponse{}, failure(err)
				}
			}
			session, err := s.store.SessionByName(ctx, targetRoom, address.Name)
			if errors.Is(err, sql.ErrNoRows) {
				return contract.PostResponse{}, invalid("unknown session @" + address.Name)
			}
			if err != nil {
				return contract.PostResponse{}, failure(err)
			}
			names[address.Name] = session.ID
		}
		ids, group := tree.Resolve(caller.Session, address, names)
		for _, target := range ids {
			recipients = append(recipients, room.Recipient{Session: target, Explicit: !group})
			if !seen[target] {
				mentions = append(mentions, contract.Principal{Kind: contract.PrincipalSession, Session: target})
				seen[target] = true
			}
		}
	}
	if surface == contract.SurfaceDM {
		recipients = append(recipients, room.Recipient{Session: req.Home.Session, Explicit: true})
	}
	event, err := s.event(req.Room, caller, contract.PostCreated{Text: req.Text, Mentions: mentions, Home: req.Home, ReplyTo: req.ReplyTo, Attachments: req.Attachments})
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	deliveries := tree.Deliveries(room.DeliveryInput{Event: event, Surface: surface, Recipients: recipients, HasMentions: len(req.Mentions) > 0, Home: req.Home, ReplyTo: req.ReplyTo})
	seq, err := s.store.Post(ctx, event, deliveries)
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
	if req.Limit < 0 || req.AfterSeq < 0 || req.BeforeSeq < 0 || (req.BeforeSeq > 0 && req.AfterSeq > 0) {
		return contract.EventsListResponse{}, invalid("invalid event range")
	}
	var dm contract.SessionID
	if req.Home != nil {
		if req.Home.Kind != contract.ConversationDM || req.Home.Session == "" {
			return contract.EventsListResponse{}, invalid("home must be a DM")
		}
		dm = req.Home.Session
	}
	events, err := s.store.EventsPage(ctx, req.Room, req.AfterSeq, req.BeforeSeq, req.Limit, req.Types, dm)
	if err != nil {
		return contract.EventsListResponse{}, failure(err)
	}
	next := req.AfterSeq
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	// Names of the sessions this page names, so that posts and events of closed sessions keep their authors.
	named := map[contract.SessionID]bool{}
	for _, event := range events {
		named[event.From.Session] = true
		switch body := event.Body.(type) {
		case contract.SessionStarted:
			named[body.Session] = true
		case contract.SessionClosed:
			named[body.Session] = true
		case contract.TurnStarted:
			named[body.Session] = true
		case contract.TurnEnded:
			named[body.Session] = true
		case contract.ShellJobEnded:
			named[body.Session] = true
		case contract.BoxChanged:
			named[body.Session] = true
		case contract.PrOpened:
			named[body.Session] = true
		case contract.OpRequested:
			named[body.By.Session] = true
		}
	}
	delete(named, "")
	var ids []any
	for id := range named {
		ids = append(ids, id)
	}
	names := map[contract.SessionID]string{}
	if len(ids) > 0 {
		rows, err := s.store.Read().QueryContext(ctx, "SELECT id,name FROM sessions WHERE id IN (?"+strings.Repeat(",?", len(ids)-1)+")", ids...)
		if err != nil {
			return contract.EventsListResponse{}, failure(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id contract.SessionID
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				return contract.EventsListResponse{}, failure(err)
			}
			names[id] = name
		}
		if err := rows.Err(); err != nil {
			return contract.EventsListResponse{}, failure(err)
		}
	}
	return contract.EventsListResponse{Events: events, Next: next, Names: names}, nil
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
		if _, err = s.transition(ctx, session.Room, current, contract.BoxReady, "", ""); err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	} else if current.State != contract.BoxReady && current.State != contract.BoxClosing {
		return contract.BoxHelloResponse{}, invalid("box is not starting, ready, or archiving")
	}
	model, _, err := s.profiles.Model(session.Profile)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	var repos []contract.BoxRepo
	if s.repos != nil {
		current, err := s.store.Box(ctx, session.Box)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
		repos, err = s.repos.Get(ctx, session.ID, current.Host, false)
		if err != nil {
			return contract.BoxHelloResponse{}, failure(err)
		}
	}
	return contract.BoxHelloResponse{Session: session.ID, Box: session.Box, Model: model, Repos: repos}, nil
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
	event, err := s.event(session.Room, caller, contract.TurnEnded{Session: session.ID, Turn: req.Turn, Outcome: req.Outcome, Final: req.Final, Usage: req.Usage, Error: req.Error, ShellJobs: req.ShellJobs})
	if err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	if err = s.store.EndTurn(ctx, session.ID, req, event); err != nil {
		return contract.TurnOutputResponse{}, failure(err)
	}
	return contract.TurnOutputResponse{}, nil
}

func (s *Service) TurnActivity(ctx context.Context, caller contract.Principal, req contract.TurnActivityRequest) (contract.TurnActivityResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.TurnActivityResponse{}, forbidden()
	}
	if !utf8.ValidString(req.Label) || strings.ContainsAny(req.Label, "\r\n") || utf8.RuneCountInString(req.Label) > 80 {
		return contract.TurnActivityResponse{}, invalid("activity label must be one line of at most 80 characters")
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.TurnActivityResponse{}, failure(err)
	}
	body := contract.TurnActivity{Session: caller.Session, Turn: req.Turn, Label: req.Label}
	event, err := s.event(session.Room, caller, body)
	if err != nil {
		return contract.TurnActivityResponse{}, failure(err)
	}
	s.mu.Lock()
	s.activity[caller.Session] = body
	for ch, room := range s.live {
		if room == session.Room {
			select {
			case ch <- event:
			default:
			}
		}
	}
	s.mu.Unlock()
	return contract.TurnActivityResponse{}, nil
}
