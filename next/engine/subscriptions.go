package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

const (
	minEvery        = time.Minute
	maxRunFiles     = 32
	maxRunFileBytes = 1 << 20
	maxRunArgs      = 64
)

// runImageName keeps a Run's image a name of ours: with no slash, colon or at sign it is no registry reference.
var runImageName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func checkRun(run *contract.RunSpec) *contract.Error {
	if slices.ContainsFunc(run.Files, func(file contract.RunFile) bool { return file.Path == box.RunEventFile }) {
		return invalid("run file " + box.RunEventFile + " is the engine's")
	}
	if len(run.Command) == 0 || len(run.Command) > maxRunArgs || run.Command[0] == "" {
		return invalid("a run needs a command of at most 64 arguments")
	}
	if !runImageName.MatchString(run.Image) {
		return invalid("a run image is base or an image folder of the room's default repository")
	}
	if run.Size != contract.BoxSizeSmall && run.Size != contract.BoxSizeAgent {
		return invalid("a run takes a small box: size small or agent")
	}
	if len(run.Files) > maxRunFiles {
		return invalid("a run has at most 32 files")
	}
	seen := make(map[string]bool)
	for _, file := range run.Files {
		// Relative to the folder the command starts in, which is all the box mounts for them.
		if file.Path == "" || !filepath.IsLocal(file.Path) || filepath.Clean(file.Path) != file.Path || seen[file.Path] {
			return invalid("each run file needs its own relative path inside the box")
		}
		if len(file.Body) > maxRunFileBytes {
			return invalid("a run file is at most 1 MiB")
		}
		seen[file.Path] = true
	}
	return nil
}

// checkEvery reads a schedule: a duration string of at least a minute.
func checkEvery(every contract.Duration) (time.Duration, *contract.Error) {
	period, err := every.Parse()
	if err != nil {
		return 0, invalid(fmt.Sprintf(`every %q is not a duration; give a string such as "1h", "15m" or "90s"`, string(every)))
	}
	if period < minEvery {
		return 0, invalid(fmt.Sprintf(`every %q is under a minute; a schedule fires at most once a minute, so every is at least "1m"`, string(every)))
	}
	return period, nil
}

func checkSubscriptionShape(req contract.SubscriptionAddRequest) *contract.Error {
	if len(req.On) == 0 && req.Every == "" {
		return invalid("set on, every, or both for a digest")
	}
	actions := 0
	if req.Wake != "" {
		actions++
	}
	if req.Run != nil {
		actions++
	}
	if req.Workflow != nil {
		actions++
	}
	if actions != 1 {
		return invalid("set exactly one of wake, run and workflow")
	}
	if len(req.On) > 0 && req.Every != "" && (req.Run != nil || req.Workflow != nil) {
		return invalid("a digest (on and every together) wakes a session; set wake, not run")
	}
	if req.Every != "" {
		if _, problem := checkEvery(req.Every); problem != nil {
			return problem
		}
	}
	for i, kind := range req.On {
		if !room.Deliverable(kind) || slices.Contains(req.On[:i], kind) {
			return invalid("unknown or repeated event type: " + string(kind) + "; dyna subscription events lists them")
		}
		// One Run must never start another, or two subscriptions can feed each other forever.
		if req.Run != nil && strings.HasPrefix(string(kind), "hook.") {
			return invalid("a run cannot fire on hook events")
		}
	}
	for _, clause := range req.Where {
		if err := contract.CheckWhere(clause); err != nil {
			return invalid(err.Error())
		}
	}
	if len(req.Where) > 0 && (len(req.On) == 0 || slices.ContainsFunc(req.On, func(kind contract.EventType) bool {
		return !strings.HasPrefix(string(kind), "github.") && !strings.HasPrefix(string(kind), "git.")
	})) {
		return invalid("where filters github.* and git.* events: dyna subscription events lists them")
	}
	if req.Workflow != nil {
		if req.Scope != contract.ScopeRoom {
			return invalid("workflow subscriptions belong to a room")
		}
		key := strings.NewReplacer("{repo}", "", "{number}", "").Replace(req.Key)
		if strings.ContainsAny(key, "{}") || len(req.Key) > 200 {
			return invalid("key uses only {repo} and {number}, at most 200 characters")
		}
	} else if req.Key != "" || req.Once || req.Report != "" || req.IncludeOwn {
		return invalid("key, once, report and includeOwn need workflow")
	}
	if req.Run != nil {
		return checkRun(req.Run)
	}
	return nil
}

// callerSession is the calling session. A closed one has no authority over a subscription.
func (s *Service) callerSession(ctx context.Context, caller contract.Principal) (store.Session, *contract.Error) {
	self, err := s.store.Session(ctx, caller.Session)
	if errors.Is(err, store.ErrNotFound) {
		return self, forbidden()
	}
	if err != nil {
		return self, failure(err)
	}
	return self, nil
}

// subscriptionSession finds the open session ref names: its ID, or its name in room, with or without an @ as a post
// mention has. With no room it looks in the caller's own room. The owner has no room of its own, so with no room the
// owner names a session by ID.
func (s *Service) subscriptionSession(ctx context.Context, caller contract.Principal, id contract.RoomID, ref string) (store.Session, *contract.Error) {
	session, err := s.store.Session(ctx, contract.SessionID(ref))
	if err == nil {
		return session, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return session, failure(err)
	}
	if id == "" && caller.Kind == contract.PrincipalSession {
		self, problem := s.callerSession(ctx, caller)
		if problem != nil {
			return self, problem
		}
		id = self.Room
	}
	if id == "" {
		return session, invalid(fmt.Sprintf("unknown session %q; with no room, name a session by its ses_… ID", ref))
	}
	session, err = s.store.SessionByName(ctx, id, strings.TrimPrefix(ref, "@"))
	if errors.Is(err, store.ErrNotFound) {
		return session, invalid(fmt.Sprintf("unknown session %q in this room", ref))
	}
	if err != nil {
		return session, failure(err)
	}
	return session, nil
}

// subscriptionAccess is the authority over a subscription. The machine is the owner's, a room is root's, and a
// session is its own and its ancestors'. A session may wake itself and whom it may address (store.receiver): its parent
// and sessions under it, or for root any session under it. A Run is the
// owner's or root's alone, because its box reads GitHub with the engine's token: root can already reach that
// token through its host grant (decision 6), and no other session can.
func (s *Service) subscriptionAccess(ctx context.Context, caller contract.Principal, sub contract.Subscription) *contract.Error {
	if caller.Kind == contract.PrincipalOwner {
		return nil
	}
	if caller.Kind != contract.PrincipalSession || sub.Scope == contract.ScopeMachine {
		return forbidden()
	}
	tree, err := s.store.RoomTree(ctx, sub.Room)
	if err != nil {
		return failure(err)
	}
	self, problem := s.callerSession(ctx, caller)
	if problem != nil {
		return problem
	}
	if self.Room != sub.Room {
		return forbidden()
	}
	root := tree.Root() == caller.Session
	if !root && (sub.Workflow != nil || sub.Run != nil || sub.Scope == contract.ScopeRoom) {
		return forbidden()
	}
	if sub.Scope == contract.ScopeSession && sub.Session != caller.Session && !tree.CanManage(caller, sub.Session) {
		return forbidden()
	}
	if sub.Wake != "" && sub.Wake != caller.Session {
		reach, err := s.store.Reaches(ctx, caller, sub.Wake)
		if err != nil {
			return failure(err)
		}
		if !reach {
			return &contract.Error{Code: contract.ErrForbidden, Message: "a session can wake only itself, its parent and sessions under its parent"}
		}
	}
	return nil
}

func (s *Service) SubscriptionAdd(ctx context.Context, caller contract.Principal, req contract.SubscriptionAddRequest) (contract.SubscriptionAddResponse, *contract.Error) {
	if problem := checkSubscriptionShape(req); problem != nil {
		return contract.SubscriptionAddResponse{}, problem
	}
	if req.Run != nil && req.Run.Host != "" {
		hosts, err := s.store.Hosts(ctx)
		if err != nil {
			return contract.SubscriptionAddResponse{}, failure(err)
		}
		if !slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == req.Run.Host && h.State != box.HostRemoved }) {
			return contract.SubscriptionAddResponse{}, invalid("unknown host")
		}
	}
	sub := contract.Subscription{Scope: req.Scope, Room: req.Room, On: req.On, Where: req.Where, Run: req.Run, Workflow: req.Workflow, Key: req.Key, Once: req.Once, Report: req.Report, IncludeOwn: req.IncludeOwn}
	if req.Every != "" {
		period, _ := checkEvery(req.Every)
		sub.Every = contract.DurationOf(period)
	}
	switch req.Scope {
	case contract.ScopeMachine:
		if req.Room != "" || req.Session != "" {
			return contract.SubscriptionAddResponse{}, invalid("scope machine takes no room and no session")
		}
		if req.Run != nil && req.Run.Image != box.BaseImage {
			return contract.SubscriptionAddResponse{}, invalid("a machine run has no repository, so its image is base")
		}
	case contract.ScopeRoom:
		if req.Room == "" {
			return contract.SubscriptionAddResponse{}, invalid("scope room needs room")
		}
		if req.Session != "" {
			return contract.SubscriptionAddResponse{}, invalid("scope room takes no session; a session's subscription is scope session")
		}
	case contract.ScopeSession:
		if req.Session == "" {
			return contract.SubscriptionAddResponse{}, invalid("scope session needs session: a session name or ID")
		}
	default:
		return contract.SubscriptionAddResponse{}, invalid("scope must be machine, room or session")
	}
	if req.Room != "" {
		if err := s.store.RoomOpen(ctx, req.Room); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return contract.SubscriptionAddResponse{}, invalid(fmt.Sprintf("unknown room %q", string(req.Room)))
			}
			return contract.SubscriptionAddResponse{}, failure(err)
		}
	}
	if req.Scope == contract.ScopeSession {
		session, problem := s.subscriptionSession(ctx, caller, req.Room, req.Session)
		if problem != nil {
			return contract.SubscriptionAddResponse{}, problem
		}
		if req.Room != "" && session.Room != req.Room {
			return contract.SubscriptionAddResponse{}, invalid(fmt.Sprintf("session %q is not in room %s", req.Session, string(req.Room)))
		}
		sub.Session, sub.Room = session.ID, session.Room
	}
	if req.Wake != "" {
		target, problem := s.subscriptionSession(ctx, caller, sub.Room, req.Wake)
		if problem != nil {
			return contract.SubscriptionAddResponse{}, problem
		}
		if req.Scope != contract.ScopeMachine && target.Room != sub.Room {
			return contract.SubscriptionAddResponse{}, invalid(fmt.Sprintf("wake %q is not in the subscription's room", req.Wake))
		}
		sub.Wake = target.ID
	}
	if problem := s.subscriptionAccess(ctx, caller, sub); problem != nil {
		return contract.SubscriptionAddResponse{}, problem
	}
	if sub.Workflow != nil {
		if _, _, problem := s.workflowScript(ctx, sub.Room, sub.Workflow.Script); problem != nil {
			return contract.SubscriptionAddResponse{}, problem
		}
		if sub.Report != "" {
			target, problem := s.subscriptionSession(ctx, caller, sub.Room, string(sub.Report))
			if problem != nil {
				return contract.SubscriptionAddResponse{}, problem
			}
			if target.Room != sub.Room {
				return contract.SubscriptionAddResponse{}, invalid("report must be in this room")
			}
			sub.Report = target.ID
		}
	}
	now := s.now()
	value, err := id("sub_", now)
	if err != nil {
		return contract.SubscriptionAddResponse{}, failure(err)
	}
	sub.ID = contract.SubscriptionID(value)
	if err := s.store.AddSubscription(ctx, sub, now); err != nil {
		if errors.Is(err, store.ErrSubscriptionTarget) {
			return contract.SubscriptionAddResponse{}, invalid(err.Error())
		}
		return contract.SubscriptionAddResponse{}, failure(err)
	}
	return contract.SubscriptionAddResponse{ID: sub.ID}, nil
}

func (s *Service) SubscriptionList(ctx context.Context, caller contract.Principal, req contract.SubscriptionListRequest) (contract.SubscriptionListResponse, *contract.Error) {
	if req.Room == "" && caller.Kind == contract.PrincipalSession {
		session, problem := s.callerSession(ctx, caller)
		if problem != nil {
			return contract.SubscriptionListResponse{}, problem
		}
		req.Room = session.Room
	}
	if req.Room == "" && caller.Kind != contract.PrincipalOwner {
		return contract.SubscriptionListResponse{}, forbidden()
	}
	if req.Room != "" {
		if problem := s.roomAccess(ctx, caller, req.Room); problem != nil {
			return contract.SubscriptionListResponse{}, problem
		}
	}
	subs, err := s.store.RoomSubscriptions(ctx, req.Room)
	if err != nil {
		return contract.SubscriptionListResponse{}, failure(err)
	}
	out := make([]contract.SubscriptionInfo, 0, len(subs))
	for _, sub := range subs {
		out = append(out, subscriptionInfo(sub))
	}
	return contract.SubscriptionListResponse{Subscriptions: out}, nil
}

// subscriptionInfo leaves out file bodies, which can be large and are not what a list is for.
func subscriptionInfo(sub store.Subscription) contract.SubscriptionInfo {
	if sub.Run != nil {
		run := *sub.Run
		run.Files = make([]contract.RunFile, len(sub.Run.Files))
		for i, file := range sub.Run.Files {
			run.Files[i].Path = file.Path
		}
		sub.Run = &run
	}
	info := contract.SubscriptionInfo{Subscription: sub.Subscription, Disabled: sub.Disabled, Default: sub.Default, Running: sub.Running, Failures: sub.Failures, Last: sub.Last}
	next := sub.DueAt
	if sub.HoldUntil.After(next) && !next.IsZero() {
		next = sub.HoldUntil
	}
	if !next.IsZero() && !sub.Disabled {
		info.NextAt = &next
	}
	if !sub.LastAt.IsZero() {
		info.LastAt = &sub.LastAt
	}
	return info
}

// ownedSubscription loads a subscription for a change. The default one stays, and only the owner turns it off.
func (s *Service) ownedSubscription(ctx context.Context, caller contract.Principal, id contract.SubscriptionID) (store.Subscription, *contract.Error) {
	sub, err := s.store.SubscriptionByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return sub, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("unknown subscription %q", string(id))}
	}
	if err != nil {
		return sub, failure(err)
	}
	if sub.Default && caller.Kind != contract.PrincipalOwner {
		return sub, forbidden()
	}
	return sub, s.subscriptionAccess(ctx, caller, sub.Subscription)
}

func (s *Service) SubscriptionRemove(ctx context.Context, caller contract.Principal, req contract.SubscriptionRemoveRequest) (contract.SubscriptionRemoveResponse, *contract.Error) {
	sub, problem := s.ownedSubscription(ctx, caller, req.ID)
	if problem != nil {
		return contract.SubscriptionRemoveResponse{}, problem
	}
	if sub.Default {
		return contract.SubscriptionRemoveResponse{}, invalid("the default subscription stays; disable it instead")
	}
	if err := s.store.RemoveSubscription(ctx, req.ID); err != nil {
		return contract.SubscriptionRemoveResponse{}, failure(err)
	}
	return contract.SubscriptionRemoveResponse{}, nil
}

func (s *Service) SubscriptionSetEnabled(ctx context.Context, caller contract.Principal, req contract.SubscriptionSetEnabledRequest) (contract.SubscriptionSetEnabledResponse, *contract.Error) {
	if _, problem := s.ownedSubscription(ctx, caller, req.ID); problem != nil {
		return contract.SubscriptionSetEnabledResponse{}, problem
	}
	if err := s.store.SetSubscriptionEnabled(ctx, req.ID, req.Enabled); err != nil {
		return contract.SubscriptionSetEnabledResponse{}, failure(err)
	}
	s.pokeSubscriptions()
	return contract.SubscriptionSetEnabledResponse{}, nil
}
