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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/lifecycle"
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
	wakeMu             sync.Mutex
	wakePending        map[contract.SessionID]bool
	wakeSignal         chan struct{}
	sessions           map[contract.SessionID]*sessionState
	live               map[chan contract.Event]contract.RoomID
	computerGeneration uint64
	activityCount      uint64
	shellGeneration    uint64
	readMu             sync.Mutex // preserves commit and live broadcast order
	// transcriptMu serializes appends, so each offset check and write runs alone.
	transcriptMu  sync.Mutex
	transcriptDir string
	filesDir      string
}

// sessionState is the engine's in-memory state for one session. s.mu guards its fields.
// computerLock serializes computer transitions and channel replacement.
// Take computerLock before s.mu, never the reverse.
type sessionState struct {
	computerLock sync.Mutex
	channel      *boxChannel
	activity     contract.TurnActivity
	computer     *computerState
	terminals    int
	watch        *shellWatch
	// activityOrder is the s.activityCount value when activity was set, so a room's newest label wins.
	activityOrder uint64
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

func New(st *store.Store, boxes lifecycle.BoxAdapter, profiles Profiles, now func() time.Time) *Service {
	s := &Service{sessions: make(map[contract.SessionID]*sessionState), live: make(map[chan contract.Event]contract.RoomID), hostexecTimeout: context.WithTimeout, store: st, boxes: boxes, profiles: profiles, now: now}
	s.reconciler = lifecycle.New(st, boxes, s, nil, s.event, now)
	s.wakePending = make(map[contract.SessionID]bool)
	s.wakeSignal = make(chan struct{}, 1)
	st.SetWake(s.queueWakes)
	go s.wakeSessions(st.Done())
	return s
}

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
	switch req.Name {
	case "root", "parent", "children", "siblings":
		// room.ParseAddress reads these names as roles, so no one could mention the session by its name.
		return contract.SessionStartResponse{}, invalid("session name " + req.Name + " is reserved for a mention role")
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
	sizeName := contract.BoxSizeAgent
	var budgetParent contract.BoxID
	if parent != "" {
		sizeName = contract.BoxSizeMedium
	}
	if parent != "" && parent != tree.Root() {
		p, err := s.store.Session(ctx, parent)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		parentBox, err := s.store.Box(ctx, p.Box)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		if parentBox.State == contract.BoxClosing || parentBox.State == contract.BoxDeleted {
			return contract.SessionStartResponse{}, invalid("parent box is closing")
		}
		budgetParent = p.Box
		sizeName = parentBox.Size.Name
	}
	initial, err := s.event(req.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending})
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	if err := s.store.StartSession(ctx, session, parent, budgetParent, sizeName, req.Goal, event, initial, ""); err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	return contract.SessionStartResponse{Session: session.ID, Box: session.Box}, nil
}

func (s *Service) Connected(id contract.SessionID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.sessions[id]
	return state != nil && state.channel != nil
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
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if !tree.CanManage(caller, session.ID) {
		return contract.SessionCloseResponse{}, forbidden()
	}
	order, err := tree.CloseOrder(session.ID)
	if err != nil {
		return contract.SessionCloseResponse{}, forbidden()
	}
	if err := s.store.MarkClosing(ctx, session.ID, req.Discard); err != nil {
		return contract.SessionCloseResponse{}, failure(err)
	}
	if !req.Discard {
		boxes, err := s.store.LifecycleBoxes(ctx)
		if err != nil {
			return contract.SessionCloseResponse{}, failure(err)
		}
		for _, b := range boxes {
			if slices.Contains(order, b.Session) && b.Want == store.WantClose && b.State == contract.BoxClosing && b.Reason != "" {
				if err := s.archiveReason(ctx, session.Room, b.Box, ""); err != nil {
					return contract.SessionCloseResponse{}, failure(err)
				}
			}
		}
	}
	s.reconciler.Kick()
	return contract.SessionCloseResponse{}, nil
}

func (s *Service) Post(ctx context.Context, caller contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.PostResponse{}, e
	}
	// A post carries text, files or both.
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return contract.PostResponse{}, invalid("post text or a file is required")
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
		if caller.Kind != contract.PrincipalOwner {
			var ok bool
			err = s.store.Read().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM uploads WHERE sha256=?1 AND room=?2) OR EXISTS(SELECT 1 FROM event_files f JOIN events e ON e.seq=f.seq WHERE f.sha256=?1 AND e.room=?2)", file.SHA256, req.Room).Scan(&ok)
			if err != nil {
				return contract.PostResponse{}, failure(err)
			}
			if !ok {
				return contract.PostResponse{}, forbidden()
			}
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
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.BoxHelloResponse{}, failure(err)
	}
	if current.State == contract.BoxCreating || current.State == contract.BoxStarting {
		_, err = s.transition(ctx, session.Room, current, contract.BoxReady, "", "")
		if errors.Is(err, sql.ErrNoRows) && current.State == contract.BoxCreating {
			current, err = s.store.Box(ctx, session.Box)
			if err == nil && current.State == contract.BoxStarting {
				_, err = s.transition(ctx, session.Room, current, contract.BoxReady, "", "")
			} else if err == nil && current.State != contract.BoxReady {
				err = sql.ErrNoRows
			}
		}
		if err != nil {
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
	rules, problem := s.RuleList(ctx, caller, contract.RuleListRequest{Room: session.Room})
	if problem != nil {
		return contract.BoxHelloResponse{}, problem
	}
	return contract.BoxHelloResponse{Session: session.ID, Box: session.Box, Model: model, Repos: repos, Rules: rules.Rules}, nil
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
	if req.Cause != "" && req.Outcome != contract.TurnFailed {
		return contract.TurnOutputResponse{}, invalid("turn cause needs a failed outcome")
	}
	switch req.Cause {
	case "", contract.CauseSetupFailed, contract.CauseModelFailed, contract.CauseOutputLimit, contract.CauseHarnessFailed:
	default:
		return contract.TurnOutputResponse{}, invalid("invalid turn cause")
	}
	req.Usage.Model = req.Model
	event, err := s.event(session.Room, caller, contract.TurnEnded{Session: session.ID, Turn: req.Turn, Outcome: req.Outcome, Final: req.Final, Usage: req.Usage, Error: req.Error, Cause: req.Cause, ShellJobs: req.ShellJobs})
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
	s.activityCount++
	state := s.sessionLocked(caller.Session)
	state.activity, state.activityOrder = body, s.activityCount
	for ch, room := range s.live {
		if room == session.Room || room == "" {
			select {
			case ch <- event:
			default:
			}
		}
	}
	s.mu.Unlock()
	return contract.TurnActivityResponse{}, nil
}

func (s *Service) ConversationRead(ctx context.Context, caller contract.Principal, req contract.ConversationReadRequest) (contract.ConversationReadResponse, *contract.Error) {
	if req.Seq < 0 || (req.Conversation.Kind != contract.ConversationFeed && req.Conversation.Kind != contract.ConversationDM) {
		return contract.ConversationReadResponse{}, invalid("invalid conversation or seq")
	}
	home := string(req.Conversation.Kind)
	if req.Conversation.Kind == contract.ConversationDM {
		session, err := s.store.Session(ctx, req.Conversation.Session)
		if err != nil {
			return contract.ConversationReadResponse{}, failure(err)
		}
		if session.Room != req.Room {
			return contract.ConversationReadResponse{}, invalid("session does not belong to room")
		}
		home += ":" + string(req.Conversation.Session)
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	var seq int64
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "INSERT INTO conversation_reads(room,conversation,seq) VALUES(?,?,?) ON CONFLICT(room,conversation) DO UPDATE SET seq=MAX(seq,excluded.seq) RETURNING seq", req.Room, home, req.Seq).Scan(&seq)
	})
	if err != nil {
		return contract.ConversationReadResponse{}, failure(err)
	}
	event, err := s.event(req.Room, caller, contract.ConversationRead{Room: req.Room, Conversation: req.Conversation, Seq: seq})
	if err != nil {
		return contract.ConversationReadResponse{}, failure(err)
	}
	s.mu.Lock()
	for ch, room := range s.live {
		if room == req.Room || room == "" {
			select {
			case ch <- event:
			default:
			}
		}
	}
	s.mu.Unlock()
	return contract.ConversationReadResponse{}, nil
}
