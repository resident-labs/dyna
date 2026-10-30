package engine

import (
	"context"
	"errors"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type ArchiveVerifier interface {
	Verify(context.Context, contract.SessionID, contract.ArchiveDoneRequest) error
}

type archiveState struct {
	reports map[contract.RepoName]store.Archive
	wake    chan struct{}
}

func (s *Service) SetArchiveVerifier(v ArchiveVerifier) { s.archiveVerifier = v }

// archiveStateLocked returns the pending reports; the caller holds s.mu.
func (s *Service) archiveStateLocked(session contract.SessionID) *archiveState {
	state := s.archives[session]
	if state == nil {
		state = &archiveState{reports: make(map[contract.RepoName]store.Archive), wake: make(chan struct{})}
		s.archives[session] = state
	}
	return state
}

func (s *Service) archiveReason(ctx context.Context, room contract.RoomID, current box.Box, reason string) error {
	event, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: current.ID, Session: current.Session, Host: current.Host, From: contract.BoxClosing, To: contract.BoxClosing, Reason: reason})
	if err != nil {
		return err
	}
	return s.store.SetClosingReason(ctx, current.ID, reason, event)
}

func (s *Service) waitArchives(ctx context.Context, session store.Session) error {
	repos, err := s.store.RoomRepos(ctx, session.Room)
	if err != nil || len(repos) == 0 {
		return err
	}
	if s.archiveVerifier == nil {
		return errors.New("archive verification unavailable")
	}
	for {
		s.mu.Lock()
		state := s.archiveStateLocked(session.ID)
		wake := state.wake
		reports := make([]store.Archive, 0, len(repos))
		for _, repo := range repos {
			report, ok := state.reports[repo.Repo]
			if !ok {
				break
			}
			reports = append(reports, report)
		}
		s.mu.Unlock()
		if len(reports) == len(repos) {
			if err := s.store.SaveArchives(ctx, reports); err != nil {
				return err
			}
			s.mu.Lock()
			delete(s.archives, session.ID)
			s.mu.Unlock()
			return nil
		}
		current, err := s.store.Box(ctx, session.Box)
		if err != nil {
			return err
		}
		if current.Reason != "" {
			return errors.New(current.Reason)
		}
		select {
		case <-wake:
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
		state := s.archiveStateLocked(session.ID)
		close(state.wake)
		state.wake = make(chan struct{})
		s.mu.Unlock()
		return contract.ArchiveDoneResponse{}, nil
	}
	ref := ""
	if !req.Skipped {
		ref = "refs/dyna/archive/" + string(session.ID)
	}
	now := s.now()
	s.mu.Lock()
	state := s.archiveStateLocked(session.ID)
	state.reports[req.Repo] = store.Archive{Session: session.ID, Repo: req.Repo, Ref: ref, Commit: req.Commit, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}
	close(state.wake)
	state.wake = make(chan struct{})
	s.mu.Unlock()
	return contract.ArchiveDoneResponse{}, nil
}
