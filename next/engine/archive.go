package engine

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// ArchiveVerifier checks the remote archive ref or published branch tip.
type ArchiveVerifier interface {
	Verify(context.Context, contract.SessionID, contract.ArchiveDoneRequest) error
}

// SetArchiveVerifier configures verification before the service starts.
func (s *Service) SetArchiveVerifier(v ArchiveVerifier) { s.archiveVerifier = v }

func (s *Service) archiveReason(ctx context.Context, room contract.RoomID, current box.Box, reason string) error {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: current.ID, Session: current.Session, Host: current.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason})
	if err != nil {
		return err
	}
	return s.store.SetClosingReason(ctx, current.ID, reason, event)
}

func (s *Service) wakeArchive(session contract.SessionID) {
	s.mu.Lock()
	if ch := s.archiveWake[session]; ch != nil {
		close(ch)
		delete(s.archiveWake, session)
	}
	s.mu.Unlock()
}

func (s *Service) waitArchives(ctx context.Context, session store.Session) error {
	defer func() {
		s.mu.Lock()
		delete(s.archiveWake, session.ID)
		s.mu.Unlock()
	}()
	for {
		s.mu.Lock()
		ch := s.archiveWake[session.ID]
		if ch == nil {
			ch = make(chan struct{})
			s.archiveWake[session.ID] = ch
		}
		s.mu.Unlock()
		repos, err := s.store.RoomRepos(ctx, session.Room)
		if err != nil {
			return err
		}
		if len(repos) == 0 {
			return nil
		}
		current, err := s.store.Box(ctx, session.Box)
		if err != nil {
			return err
		}
		if current.Reason != "" {
			return errors.New(current.Reason)
		}
		if s.archiveVerifier == nil {
			err = errors.New("archive verification unavailable")
			if saveErr := s.archiveReason(context.WithoutCancel(ctx), session.Room, current, err.Error()); saveErr != nil {
				return saveErr
			}
			return err
		}
		archives, err := s.store.Archives(ctx, session.ID)
		if err != nil {
			return err
		}
		done := make(map[contract.RepoName]bool, len(archives))
		for _, item := range archives {
			done[item.Repo] = true
		}
		complete := true
		for _, repo := range repos {
			if !done[repo.Repo] {
				complete = false
				break
			}
		}
		if complete {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
	repos, err := s.store.RoomRepos(ctx, session.Room)
	if err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	found := false
	for _, repo := range repos {
		if repo.Repo == req.Repo {
			found = true
			break
		}
	}
	if !found {
		return contract.ArchiveDoneResponse{}, forbidden()
	}
	reason := req.Error
	if reason == "" && req.Commit == "" {
		reason = "archive commit is missing"
	}
	if reason == "" {
		if s.archiveVerifier == nil {
			reason = "archive verification unavailable"
		} else if err := s.archiveVerifier.Verify(ctx, session.ID, req); err != nil {
			reason = err.Error()
		}
	}
	if reason != "" {
		if err := s.archiveReason(ctx, session.Room, current, reason); err != nil {
			return contract.ArchiveDoneResponse{}, failure(err)
		}
		s.mu.Lock()
		if channel := s.channels[session.ID]; channel != nil {
			channel.stop()
		}
		s.mu.Unlock()
		s.wakeArchive(session.ID)
		return contract.ArchiveDoneResponse{}, nil
	}
	ref := ""
	if !req.Skipped {
		ref = "refs/dyna/archive/" + string(session.ID)
	}
	now := s.now()
	if err := s.store.SaveArchive(ctx, store.Archive{Session: session.ID, Repo: req.Repo, Ref: ref, Commit: req.Commit, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}); err != nil {
		return contract.ArchiveDoneResponse{}, failure(err)
	}
	s.wakeArchive(session.ID)
	return contract.ArchiveDoneResponse{}, nil
}
