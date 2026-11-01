package engine

import (
	"context"
	"errors"
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

func checkSubscriptionShape(req contract.SubscriptionAddRequest) *contract.Error {
	if (len(req.On) > 0) == (req.Every > 0) || req.Every < 0 {
		return invalid("set exactly one of on and every")
	}
	if (req.Wake != nil) == (req.Run != nil) {
		return invalid("set exactly one of wake and run")
	}
	if req.Every > 0 && req.Every < contract.DurationMS(minEvery.Milliseconds()) {
		return invalid("a schedule fires at most once a minute")
	}
	for i, kind := range req.On {
		if !room.Deliverable(kind) || slices.Contains(req.On[:i], kind) {
			return invalid("unknown or repeated event type: " + string(kind))
		}
		// One Run must never start another, or two subscriptions can feed each other forever.
		if req.Run != nil && strings.HasPrefix(string(kind), "hook.") {
			return invalid("a run cannot fire on hook events")
		}
	}
	if req.Wake != nil && (req.Wake.Kind != contract.PrincipalSession || req.Wake.Session == "") {
		return invalid("wake must name a session")
	}
	if req.Run != nil {
		return checkRun(req.Run)
	}
	return nil
}

// subscriptionAccess is the authority over a subscription. The machine is the owner's, a room is root's, and a
// session is its own and its ancestors'. Waking a session takes the same authority over it. A Run is the
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
	self, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return failure(err)
	}
	if self.Room != sub.Room {
		return forbidden()
	}
	root := tree.Root() == caller.Session
	manages := func(target contract.SessionID) bool {
		return target == caller.Session || tree.CanManage(caller, target)
	}
	if sub.Run != nil && !root {
		return forbidden()
	}
	if sub.Scope == contract.ScopeRoom && !root {
		return forbidden()
	}
	if sub.Scope == contract.ScopeSession && !manages(sub.Session) {
		return forbidden()
	}
	if sub.Wake != nil && !manages(sub.Wake.Session) {
		return forbidden()
	}
	return nil
}

func (s *Service) SubscriptionAdd(ctx context.Context, caller contract.Principal, req contract.SubscriptionAddRequest) (contract.SubscriptionAddResponse, *contract.Error) {
	if problem := checkSubscriptionShape(req); problem != nil {
		return contract.SubscriptionAddResponse{}, problem
	}
	sub := contract.Subscription{Scope: req.Scope, Room: req.Room, Session: req.Session, On: req.On, Every: req.Every, Wake: req.Wake, Run: req.Run}
	switch req.Scope {
	case contract.ScopeMachine:
		if req.Room != "" || req.Session != "" {
			return contract.SubscriptionAddResponse{}, invalid("a machine subscription names no room or session")
		}
		if req.Run != nil && req.Run.Image != box.BaseImage {
			return contract.SubscriptionAddResponse{}, invalid("a machine run has no repository, so its image is base")
		}
	case contract.ScopeRoom:
		if req.Room == "" || req.Session != "" {
			return contract.SubscriptionAddResponse{}, invalid("a room subscription names its room and no session")
		}
		if err := s.store.RoomOpen(ctx, req.Room); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return contract.SubscriptionAddResponse{}, invalid("unknown room")
			}
			return contract.SubscriptionAddResponse{}, failure(err)
		}
	case contract.ScopeSession:
		if req.Session == "" || req.Room != "" {
			return contract.SubscriptionAddResponse{}, invalid("a session subscription names its session and no room")
		}
		session, err := s.store.Session(ctx, req.Session)
		if err != nil {
			return contract.SubscriptionAddResponse{}, failure(err)
		}
		sub.Room = session.Room
	default:
		return contract.SubscriptionAddResponse{}, invalid("scope must be machine, room or session")
	}
	if req.Wake != nil {
		target, err := s.store.Session(ctx, req.Wake.Session)
		if err != nil {
			return contract.SubscriptionAddResponse{}, failure(err)
		}
		if req.Scope != contract.ScopeMachine && target.Room != sub.Room {
			return contract.SubscriptionAddResponse{}, invalid("wake must name a session in the subscription's room")
		}
	}
	if problem := s.subscriptionAccess(ctx, caller, sub); problem != nil {
		return contract.SubscriptionAddResponse{}, problem
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
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.SubscriptionListResponse{}, failure(err)
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
