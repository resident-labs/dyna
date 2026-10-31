package engine

import (
	"context"
	"slices"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

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
	if req.Size != "" {
		if parent != "" && parent != tree.Root() {
			return contract.SessionStartResponse{}, invalid("a child keeps its parent's size")
		}
		sizeName = req.Size
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
