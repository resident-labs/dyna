package engine

import (
	"context"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// DiffSaved is a closing box's one report of the diff it wrote (spec 5.9). Whatever it says, the box is removed next. The notice
// goes to the parent once, whichever way the report or the timeout comes first.
func (s *Service) DiffSaved(ctx context.Context, caller contract.Principal, req contract.DiffSavedRequest) (contract.DiffSavedResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.DiffSavedResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.DiffSavedResponse{}, failure(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.DiffSavedResponse{}, failure(err)
	}
	if current.State != contract.BoxClosing {
		return contract.DiffSavedResponse{}, invalid("box is not closing")
	}
	if session.Repo != "" {
		tree, err := s.store.RoomTree(ctx, session.Room)
		if err != nil {
			return contract.DiffSavedResponse{}, failure(err)
		}
		text := req.CloseNotice(session.Name)
		if len(text) > 4096 {
			text = strings.ToValidUTF8(text[:4096], "")
		}
		notice, err := s.engineEvent(session.Room, contract.SessionNotice{Session: session.ID, Text: text, ClientID: "close-diff:" + string(session.Box)})
		if err != nil {
			return contract.DiffSavedResponse{}, failure(err)
		}
		if err := s.store.SendSessionNotice(ctx, notice, tree.Parent(session.ID)); err != nil {
			return contract.DiffSavedResponse{}, failure(err)
		}
	}
	if err := s.store.RequestRemoval(ctx, session.ID); err != nil {
		return contract.DiffSavedResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.DiffSavedResponse{}, nil
}
