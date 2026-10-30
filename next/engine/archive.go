package engine

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) archiveReason(ctx context.Context, room contract.RoomID, current box.Box, reason string) error {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: current.ID, Session: current.Session, Host: current.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason})
	if err != nil {
		return err
	}
	return s.store.SetClosingReason(ctx, current.ID, reason, event)
}

// ResumeClosures finishes interrupted closes that no longer need an archive.
func (s *Service) ResumeClosures(ctx context.Context) *contract.Error {
	boxes, err := s.store.Boxes(ctx)
	if err != nil {
		return failure(err)
	}
	for _, current := range boxes {
		if current.State != contract.BoxClosing && current.State != contract.BoxDeleted {
			continue
		}
		session, err := s.store.Session(ctx, current.Session)
		if current.State == contract.BoxDeleted && errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return failure(err)
		}
		if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session.ID}); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) ArchiveDone(ctx context.Context, caller contract.Principal, req contract.ArchiveDoneRequest) (contract.ArchiveDoneResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalSession {
		return contract.ArchiveDoneResponse{}, forbidden()
	}
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	unlock := s.lockBox(session.ID)
	defer unlock()
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
	repos, err := s.store.RoomRepos(ctx, session.Room)
	if err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	if !slices.ContainsFunc(repos, func(repo contract.RoomRepo) bool { return repo.Repo == req.Repo }) {
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
		s.mu.Lock()
		delete(s.archives, session.ID)
		s.mu.Unlock()
		return contract.ArchiveDoneResponse{}, nil
	}
	if req.Skipped {
		ref = ""
	}
	now := s.now()
	s.mu.Lock()
	state := s.archives[session.ID]
	if state == nil {
		state = make(map[contract.RepoName]store.Archive)
		s.archives[session.ID] = state
	}
	state[req.Repo] = store.Archive{Session: session.ID, Repo: req.Repo, Ref: ref, Commit: req.Commit, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}
	reports := make([]store.Archive, 0, len(repos))
	for _, repo := range repos {
		report, ok := state[repo.Repo]
		if !ok {
			break
		}
		reports = append(reports, report)
	}
	s.mu.Unlock()
	if len(reports) == len(repos) {
		if err := s.store.SaveArchives(ctx, reports); err != nil {
			return contract.ArchiveDoneResponse{}, failure(err)
		}
		if err := s.finishClose(context.WithoutCancel(ctx), session, current, contract.Principal{Kind: contract.PrincipalEngine}); err != nil {
			return contract.ArchiveDoneResponse{}, err
		}
	}
	return contract.ArchiveDoneResponse{}, nil
}
