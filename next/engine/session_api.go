package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

func (s *Service) SessionStart(ctx context.Context, caller contract.Principal, req contract.SessionStartRequest) (contract.SessionStartResponse, *contract.Error) {
	if len(req.ClientID) > 64 {
		return contract.SessionStartResponse{}, invalid("clientId is longer than 64 characters")
	}
	if e := checkGoalText(req.Goal, req.DoneWhen); e != nil {
		return contract.SessionStartResponse{}, e
	}
	if !mentionName.MatchString(req.Name) {
		return contract.SessionStartResponse{}, invalid("session name must be 1–64 letters, digits, dots, underscores or hyphens and start with a letter or digit")
	}
	if sessionID.MatchString(req.Name) {
		return contract.SessionStartResponse{}, invalid("session name cannot be a session ID")
	}
	switch req.Name {
	case "root", "parent", "children", "siblings":
		// room.ParseAddress reads these names as roles, so no one could mention the session by its name.
		return contract.SessionStartResponse{}, invalid("session name " + req.Name + " is reserved for a mention role")
	}
	return s.startSession(ctx, caller, req, "", nil)
}

func (s *Service) startSession(ctx context.Context, caller contract.Principal, req contract.SessionStartRequest, roomName string, run *store.WorkflowRun) (contract.SessionStartResponse, *contract.Error) {
	if roomName == "" {
		if e := s.roomAccess(ctx, caller, req.Room); e != nil {
			return contract.SessionStartResponse{}, e
		}
	}
	if req.ClientID != "" {
		response, err := s.store.SessionReplay(ctx, caller, req.ClientID)
		if err == nil {
			return response, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return contract.SessionStartResponse{}, failure(err)
		}
	}

	if req.Name == "" || req.Profile == "" {
		return contract.SessionStartResponse{}, invalid("name and profile are required")
	}
	if _, ok := box.Size(req.Size); req.Size != "" && !ok {
		return contract.SessionStartResponse{}, invalid("unknown size")
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
		if req.Parent != "" {
			named, problem := s.resolveSession(ctx, caller, req.Parent)
			if problem != nil {
				return contract.SessionStartResponse{}, problem
			}
			req.Parent = named.ID
		}
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
	var composeName string
	var composeFile *compose.File
	if roomName == "" {
		var problem *contract.Error
		if composeName, composeFile, problem = s.startCompose(ctx, req, repo); problem != nil {
			return contract.SessionStartResponse{}, problem
		}
	}
	session := store.Session{ID: contract.SessionID(sid), Room: req.Room, Name: req.Name, Repo: repo, Profile: req.Profile, Box: contract.BoxID(bid), NewRoomName: roomName, Compose: composeName, Workflow: run}
	event, err := s.event(req.Room, caller, contract.SessionStarted{Session: session.ID, Name: req.Name, Parent: parent, Profile: req.Profile, Goal: req.Goal, DoneWhen: req.DoneWhen, Brief: req.Brief, Workflow: run != nil})
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	sizeName := contract.BoxSizeAgent
	if parent != "" && parent == tree.Root() {
		sizeName = contract.BoxSizeMedium
	}
	if req.Size != "" {
		sizeName = req.Size
	} else if s.images != nil { // set with the layout
		if s.migrator != nil {
			s.migrator.Room(ctx, req.Room, true)
		}
		wanted := ""
		if composeFile != nil {
			wanted = composeFile.Agent.Build
		}
		sizeText, err := s.images.Size(req.Room, wanted)
		if err != nil {
			return contract.SessionStartResponse{}, failure(err)
		}
		size := contract.BoxSizeName(sizeText)
		if _, ok := box.Size(size); size != "" && !ok {
			return contract.SessionStartResponse{}, invalid("the image's size file is not a size")
		}
		if size != "" {
			sizeName = size
		}
	}
	if composeFile != nil {
		size, _ := box.Size(sizeName)
		if err := composeFile.CheckLimits(size); err != nil {
			return contract.SessionStartResponse{}, composeProblem(err)
		}
	}
	initial, err := s.event(req.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending})
	if err != nil {
		return contract.SessionStartResponse{}, failure(err)
	}
	var response contract.SessionStartResponse
	problem := s.withProfile(req.Profile, func() *contract.Error {
		var err error
		response, err = s.store.StartSession(ctx, session, parent, sizeName, req.Goal, event, initial, "", req.SameHost, req.ClientID, s.maxDepth)
		if err != nil {
			var treeErr *store.TreeError
			if errors.As(err, &treeErr) {
				return invalid(treeErr.Error())
			}
			if errors.Is(err, store.ErrParentHostAbsent) {
				return invalid(err.Error())
			}
			return failure(err)
		}
		return nil
	})
	if problem != nil {
		return contract.SessionStartResponse{}, problem
	}
	return response, nil
}

// startCompose chooses a new session's compose file. Only a child takes one: a room's root starts plain.
func (s *Service) startCompose(ctx context.Context, req contract.SessionStartRequest, repo contract.RepoName) (string, *compose.File, *contract.Error) {
	if s.compose == nil {
		if req.Compose != "" {
			return "", nil, invalid("compose files are not available here")
		}
		return "", nil, nil
	}
	name, file, err := s.compose.Start(ctx, req.Room, repo, req.Compose)
	if err != nil {
		return "", nil, composeProblem(err)
	}
	return name, file, nil
}

// composeProblem makes a refused file the caller's to fix, and anything else the engine's.
func composeProblem(err error) *contract.Error {
	var refused box.StackError
	if errors.As(err, &refused) {
		return invalid(refused.Error())
	}
	return failure(err)
}

// ComposeList lists the compose files a child session could start with.
func (s *Service) ComposeList(ctx context.Context, caller contract.Principal, req contract.ComposeListRequest) (contract.ComposeListResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.ComposeListResponse{}, e
	}
	repos, err := s.store.RoomRepos(ctx, req.Room)
	if err != nil {
		return contract.ComposeListResponse{}, failure(err)
	}
	var own contract.RepoName
	if caller.Kind == contract.PrincipalSession {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.ComposeListResponse{}, failure(err)
		}
		own = session.Repo
	}
	repo, err := room.RepoForSession(repos, req.Repo, own)
	if err != nil {
		return contract.ComposeListResponse{}, invalid(err.Error())
	}
	response := contract.ComposeListResponse{Files: []contract.ComposeFile{}}
	if s.compose == nil {
		return response, nil
	}
	if response.Files, err = s.compose.List(ctx, req.Room, repo); err != nil {
		return contract.ComposeListResponse{}, failure(err)
	}
	return response, nil
}

func (s *Service) Connected(id contract.SessionID) bool {
	handle, ok := s.live.LookupSession(id)
	if !ok {
		return false
	}
	connected, _ := handle.Connected()
	return connected
}

// Stale reports whether a session's box runs an older box agent than the engine or was marked to restart. Boxes report on every
// connection, so the first is rebuilt as they reconnect after an engine restart. A mark is only in memory and is forgotten.
func (s *Service) Stale(id contract.SessionID) bool {
	handle, ok := s.live.LookupSession(id)
	return ok && handle.Stale()
}

// resolveSession resolves a session ID or a name in the caller's room.
func (s *Service) resolveSession(ctx context.Context, caller contract.Principal, id contract.SessionID) (store.Session, *contract.Error) {
	if !sessionID.MatchString(string(id)) && caller.Kind == contract.PrincipalSession {
		self, problem := s.callerSession(ctx, caller)
		if problem != nil {
			return store.Session{}, problem
		}
		session, err := s.store.SessionByName(ctx, self.Room, string(id))
		if errors.Is(err, store.ErrNotFound) {
			return store.Session{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("no open session named %q in this room", id)}
		}
		if err != nil {
			return store.Session{}, failure(err)
		}
		return session, nil
	}
	session, err := s.store.Session(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("session %s is closed or does not exist", id)}
	}
	if err != nil {
		return store.Session{}, failure(err)
	}
	return session, nil
}

// managedSession loads a target and checks the caller's authority over it.
func (s *Service) managedSession(ctx context.Context, caller contract.Principal, id contract.SessionID) (store.Session, *contract.Error) {
	session, problem := s.resolveSession(ctx, caller, id)
	if problem != nil {
		return store.Session{}, problem
	}
	if problem := s.roomAccess(ctx, caller, session.Room); problem != nil {
		return store.Session{}, problem
	}
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return store.Session{}, failure(err)
	}
	if !tree.CanManage(caller, session.ID) {
		return store.Session{}, forbidden()
	}
	return session, nil
}

func (s *Service) SessionClose(ctx context.Context, caller contract.Principal, req contract.SessionCloseRequest) (contract.SessionCloseResponse, *contract.Error) {
	session, problem := s.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return contract.SessionCloseResponse{}, problem
	}
	return s.closeSession(ctx, session, false, false)
}

// SessionResponsibilities reads a session's list and what a close would do with it now.
func (s *Service) SessionResponsibilities(ctx context.Context, caller contract.Principal, req contract.SessionResponsibilitiesRequest) (contract.SessionResponsibilitiesResponse, *contract.Error) {
	var response contract.SessionResponsibilitiesResponse
	session, problem := s.resolveSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	if problem := s.roomAccess(ctx, caller, session.Room); problem != nil {
		return response, problem
	}
	held, err := s.store.Responsibilities(ctx, session.ID)
	if err != nil {
		return response, failure(err)
	}
	response.Items = append([]contract.Responsibility{}, held...)
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return response, failure(err)
	}
	if order, err := tree.CloseOrder(session.ID); err == nil {
		if response.Close, err = s.store.ClosePlan(ctx, order, tree.Parent(session.ID), tree.Parent(session.ID) == tree.Root()); err != nil {
			return response, failure(err)
		}
	}
	return response, nil
}

// closeSession never refuses over what a session holds: the list says what the close does with it, and the response says the same.
func (s *Service) closeSession(ctx context.Context, session store.Session, discard, dormant bool) (contract.SessionCloseResponse, *contract.Error) {
	var plan contract.SessionCloseResponse
	tree, err := s.store.RoomTree(ctx, session.Room)
	if err != nil {
		return plan, failure(err)
	}
	order, err := tree.CloseOrder(session.ID)
	if err != nil {
		return plan, forbidden()
	}
	if plan, err = s.store.ClosePlan(ctx, order, tree.Parent(session.ID), tree.Parent(session.ID) == tree.Root()); err != nil {
		return plan, failure(err)
	}
	if dormant {
		changed, err := s.store.MarkDormantClosing(ctx, session.ID, s.now().Add(-7*24*time.Hour), s.now().Add(-diskLowWait))
		if err != nil {
			return plan, failure(err)
		}
		if !changed {
			return plan, nil
		}
	} else if err := s.store.MarkClosing(ctx, session.ID, discard, s.now()); err != nil {
		return plan, failure(err)
	}
	if err := s.stopWorkflows(ctx, order); err != nil {
		return plan, failure(err)
	}
	if err := s.StopHostexecs(ctx); err != nil {
		return plan, failure(err)
	}
	s.reconciler.Kick()
	return plan, nil
}

// SessionRestart retries an open session whose box has failed.
func (s *Service) SessionRestart(ctx context.Context, caller contract.Principal, req contract.SessionRestartRequest) (contract.SessionRestartResponse, *contract.Error) {
	session, problem := s.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return contract.SessionRestartResponse{}, problem
	}
	event, err := s.event(session.Room, caller, contract.BoxChanged{})
	if err != nil {
		return contract.SessionRestartResponse{}, failure(err)
	}
	notice, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionRestarted{Session: session.ID})
	if err != nil {
		return contract.SessionRestartResponse{}, failure(err)
	}
	restarted, err := s.store.RestartSession(ctx, session.ID, event, notice)
	if err != nil {
		return contract.SessionRestartResponse{}, failure(err)
	}
	if !restarted {
		return contract.SessionRestartResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "session is closing or its box has not failed"}
	}
	s.reconciler.Kick()
	return contract.SessionRestartResponse{}, nil
}

// SessionRefresh marks running boxes stale, so the rule for an old binary applies to them whatever they run. A box that is not
// running needs nothing, since it starts fresh. The mark lasts until a new box agent says hello. A session may mark itself by
// name, and it then comes back on its own (BoxStopped), since it might otherwise wait for a message that never comes.
func (s *Service) SessionRefresh(ctx context.Context, caller contract.Principal, req contract.SessionRefreshRequest) (contract.SessionRefreshResponse, *contract.Error) {
	response := contract.SessionRefreshResponse{Marked: []contract.SessionID{}}
	if (req.Session == "") == (req.Room == "") {
		return response, invalid("give a session or a room")
	}
	if req.Session != "" {
		session, problem := s.resolveSession(ctx, caller, req.Session)
		if problem != nil {
			return response, problem
		}
		req.Session = session.ID
	}
	self := caller.Kind == contract.PrincipalSession && req.Session == caller.Session
	roomID := req.Room
	switch {
	case self:
		session, err := s.store.Session(ctx, req.Session)
		if err != nil {
			return response, failure(err)
		}
		roomID = session.Room
	case req.Session != "":
		session, problem := s.managedSession(ctx, caller, req.Session)
		if problem != nil {
			return response, problem
		}
		roomID = session.Room
	default:
		if problem := s.roomAccess(ctx, caller, roomID); problem != nil {
			return response, problem
		}
	}
	tree, err := s.store.RoomTree(ctx, roomID)
	if err != nil {
		return response, failure(err)
	}
	if tree.Root() == "" {
		return response, &contract.Error{Code: contract.ErrNotFound, Message: "room not found"}
	}
	running, err := s.store.RunningSessions(ctx, roomID)
	if err != nil {
		return response, failure(err)
	}
	for _, target := range running {
		if req.Session != "" && target != req.Session || !self && !tree.CanManage(caller, target) {
			continue
		}
		mark, err := id("refresh_", s.now())
		if err != nil {
			return response, failure(err)
		}
		handle, problem := s.lockSession(ctx, target)
		if problem != nil {
			if problem.Code == contract.ErrNotFound {
				continue // It closed just now, so it needs nothing.
			}
			return response, problem
		}
		handle.MarkRefresh(mark, self)
		if channel := handle.Channel(); channel != nil {
			channel.Wake()
		}
		handle.Unlock()
		response.Marked = append(response.Marked, target)
	}
	s.reconciler.Kick()
	return response, nil
}

// BoxStopped starts a box that asked to restart itself straight back up. Its wake waits for the stop because Dispatch steers a
// wake into an open turn, so an earlier one would be read on the old binary and leave nothing to start the box. Held past its
// turn, the box then needs nothing different. It carries an update notice's ID, so it goes once per mark, however often the box
// stops before it says hello, and a close does not pass it to the parent.
func (s *Service) BoxStopped(ctx context.Context, id contract.SessionID) {
	handle, ok := s.live.LookupSession(id)
	if !ok {
		return
	}
	mark := handle.RefreshWake()
	if mark == "" {
		return
	}
	session, err := s.store.Session(ctx, id)
	if err != nil {
		log.Printf("session %s: box restart: %v", id, err)
		return
	}
	notice, err := s.engineEvent(session.Room, contract.SessionNotice{Session: id, Text: room.BoxRestartedNotice, ClientID: store.BoxUpdateNoticeID(session.Box, "restarted:"+mark)})
	if err == nil {
		err = s.store.WakeWithNotice(ctx, notice, id)
	}
	if err != nil {
		log.Printf("session %s: box restart: %v", id, err)
	}
}

// SessionSetBranch makes branch the session's branch on its repository, which the owner does to move a live session to a new
// name. The session's earlier branches stay its own, and it gets a notice with the new one. Nothing is pushed or renamed.
func (s *Service) SessionSetBranch(ctx context.Context, caller contract.Principal, req contract.SessionSetBranchRequest) (contract.SessionSetBranchResponse, *contract.Error) {
	var response contract.SessionSetBranchResponse
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	target, problem := s.resolveSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	if target.Repo == "" {
		return response, invalid("the session has no repository")
	}
	if !enginegit.ValidBranch(req.Branch) {
		return response, invalid("branch must be letters, digits, dots, underscores or hyphens, start with a letter or digit, not end in . or .lock, not hold ..")
	}
	unavailable, err := s.repos.Unavailable(ctx, target.Repo)
	if err != nil {
		return response, failure(err)
	}
	if unavailable[req.Branch] {
		return response, invalid("branch " + req.Branch + " is main, master, HEAD, the repository's default branch or a branch on GitHub; no session may have it")
	}
	if err := s.store.AddBranch(ctx, target.Repo, target.ID, req.Branch, time.Now()); err != nil {
		if errors.Is(err, store.ErrBranchTaken) {
			return response, invalid("another session has that branch")
		}
		return response, failure(err)
	}
	text := "Your branch is now " + req.Branch + " on " + string(target.Repo) + ". Your earlier branches stay yours: work and PRs on them still push. Nothing was renamed on GitHub; `git branch -m " + req.Branch + "` and push when you move the work."
	notice, err := s.engineEvent(target.Room, contract.SessionNotice{Session: target.ID, Text: text, ClientID: "set-branch:" + req.Branch})
	if err == nil {
		err = s.store.SendSessionNotice(ctx, notice, target.ID)
	}
	if err != nil {
		return response, failure(err)
	}
	return response, nil
}

// SessionSetProfile moves a session to another profile. A failed session wakes to carry its failed input with it.
func (s *Service) SessionSetProfile(ctx context.Context, caller contract.Principal, req contract.SessionSetProfileRequest) (contract.SessionSetProfileResponse, *contract.Error) {
	var response contract.SessionSetProfileResponse
	target, problem := s.managedSession(ctx, caller, req.Session)
	if problem != nil {
		return response, problem
	}
	if _, _, err := s.profiles.Model(req.Profile); err != nil {
		return response, invalid("unknown profile")
	}
	event, err := s.event(target.Room, caller, contract.SessionProfileChanged{Session: target.ID, Profile: req.Profile})
	if err != nil {
		return response, failure(err)
	}
	problem = s.withProfile(req.Profile, func() *contract.Error {
		if err := s.store.SetSessionProfile(ctx, target.ID, req.Profile, event); err != nil {
			if errors.Is(err, store.ErrTreeForbidden) {
				return forbidden()
			}
			return failure(err)
		}
		return nil
	})
	if problem == nil {
		s.sessionsMoved(ctx, []store.Session{target}, req.Profile)
	}
	return response, problem
}
