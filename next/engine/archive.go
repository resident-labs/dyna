package engine

import (
	"context"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) archiveReason(ctx context.Context, room contract.RoomID, current box.Box, reason string) error {
	var cause contract.FailureCause
	if reason != "" {
		cause = contract.CauseArchiveFailed
	}
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: current.ID, Session: current.Session, Host: current.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason, Cause: cause})
	if err != nil {
		return err
	}
	return s.store.SetClosingReason(ctx, current.ID, reason, event)
}

func (s *Service) ArchiveDone(ctx context.Context, caller contract.Principal, req contract.ArchiveDoneRequest) (contract.ArchiveDoneResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ArchiveDoneResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	current, err := s.store.Box(ctx, session.Box)
	if err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	if current.State != contract.BoxClosing {
		return contract.ArchiveDoneResponse{}, invalid("box is not closing")
	}
	if current.Reason != "" {
		return contract.ArchiveDoneResponse{}, nil
	}
	if session.Repo == "" && req.Repo == "" {
		if err := s.store.CompleteArchives(ctx, session.ID, nil); err != nil {
			return contract.ArchiveDoneResponse{}, failure(err)
		}
		s.reconciler.Kick()
		return contract.ArchiveDoneResponse{}, nil
	}
	if session.Repo == "" || session.Repo != req.Repo {
		return contract.ArchiveDoneResponse{}, forbidden()
	}
	reason := req.Error
	if reason == "" && req.Commit == "" {
		reason = "archive commit is missing"
	}
	ref := "refs/dyna/archive/" + string(session.ID)
	if req.Skipped {
		ref = "refs/heads/" + enginegit.SessionBranchPrefix + string(session.ID)
	}
	if reason == "" {
		hash, err := s.repos.Ref(ctx, req.Repo, ref)
		if err != nil {
			reason = err.Error()
		} else if hash != req.Commit {
			reason = "archive ref mismatch"
		}
	}
	if reason != "" {
		if err := s.archiveReason(ctx, session.Room, current, reason); err != nil {
			return contract.ArchiveDoneResponse{}, failure(err)
		}
		return contract.ArchiveDoneResponse{}, nil
	}
	if req.Skipped {
		ref = ""
	}
	now := s.now()
	report := store.Archive{Session: session.ID, Repo: req.Repo, Ref: ref, Commit: req.Commit, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}
	if err := s.store.CompleteArchives(ctx, session.ID, []store.Archive{report}); err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	s.reconciler.Kick()
	return contract.ArchiveDoneResponse{}, nil
}
