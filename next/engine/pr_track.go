package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

// SetPRTracking connects the GitHub reader and the poll.
func (s *Service) SetPRTracking(pulls *prs.Client, poll *prs.Poller) {
	s.pulls = pulls
	s.prPoll = poll
}

func (s *Service) resetPRPoll(repo contract.RepoName) {
	if s.prPoll != nil {
		s.prPoll.Reset(repo)
	}
}

// prFor returns a PR for this caller. A title by anyone but the owner's account reaches a session wrapped (decision 13).
// A PR with no author yet came from pr.open and no poll has read it, so its title is the session's own.
func prFor(caller contract.Principal, p store.TrackedPR, viewer string) contract.PrSummary {
	summary := p.PrSummary
	if caller.Kind == contract.PrincipalSession && summary.Author != "" && summary.Author != viewer {
		summary.Title = room.WrapExternal(summary.Author, summary.Title)
	}
	return summary
}

// prViewer is the login that counts as the owner's: the one the poll last saw. Before any poll nobody counts, so every title is wrapped.
func (s *Service) prViewer() string {
	if s.pulls == nil {
		return ""
	}
	return s.pulls.Viewer()
}

// prRepo fills in the caller's session repository.
func (s *Service) prRepo(ctx context.Context, caller contract.Principal, repo contract.RepoName, number int) (contract.RepoName, *contract.Error) {
	if number <= 0 {
		return "", invalid("number must be positive")
	}
	if repo == "" {
		if caller.Kind != contract.PrincipalSession {
			return "", invalid("repo is required")
		}
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return "", failure(err)
		}
		repo = session.Repo
	}
	if !prs.ValidRepo(repo) {
		return "", invalid("repo must be owner/name")
	}
	return repo, nil
}

// trackedPR reads a PR the caller may see: the owner sees all, and a session those of its room.
func (s *Service) trackedPR(ctx context.Context, caller contract.Principal, repo contract.RepoName, number int) (store.TrackedPR, *contract.Error) {
	pr, err := s.store.PR(ctx, repo, number)
	if errors.Is(err, store.ErrNotFound) {
		return pr, &contract.Error{Code: contract.ErrNotFound, Message: "pull request is not tracked"}
	}
	if err != nil {
		return pr, failure(err)
	}
	return pr, s.roomAccess(ctx, caller, pr.Room)
}

func (s *Service) PrList(ctx context.Context, caller contract.Principal, req contract.PrListRequest) (contract.PrListResponse, *contract.Error) {
	target := req.Room
	if caller.Kind == contract.PrincipalSession && target == "" {
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return contract.PrListResponse{}, failure(err)
		}
		target = session.Room
	}
	if target != "" || caller.Kind != contract.PrincipalOwner {
		if e := s.roomAccess(ctx, caller, target); e != nil {
			return contract.PrListResponse{}, e
		}
	}
	rows, err := s.store.RoomPRs(ctx, target, req.All)
	if err != nil {
		return contract.PrListResponse{}, failure(err)
	}
	result := contract.PrListResponse{PRs: make([]contract.PrSummary, 0, len(rows))}
	viewer := s.prViewer()
	for _, row := range rows {
		result.PRs = append(result.PRs, prFor(caller, row, viewer))
	}
	return result, nil
}

func (s *Service) PrStatus(ctx context.Context, caller contract.Principal, req contract.PrStatusRequest) (contract.PrStatusResponse, *contract.Error) {
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.PrStatusResponse{}, e
	}
	pr, e := s.trackedPR(ctx, caller, repo, req.Number)
	if e != nil {
		return contract.PrStatusResponse{}, e
	}
	return contract.PrStatusResponse{PR: prFor(caller, pr, s.prViewer())}, nil
}

// prTracker finds the session that will own a newly tracked PR.
func (s *Service) prTracker(ctx context.Context, caller contract.Principal, req contract.PrTrackRequest) (store.Session, *contract.Error) {
	id := req.Session
	switch caller.Kind {
	case contract.PrincipalSession:
		if (id != "" && id != caller.Session) || req.Room != "" {
			return store.Session{}, forbidden()
		}
		id = caller.Session
	case contract.PrincipalOwner:
		if id == "" {
			if req.Room == "" {
				return store.Session{}, invalid("name a room or a session")
			}
			tree, err := s.store.RoomTree(ctx, req.Room)
			if err != nil {
				return store.Session{}, failure(err)
			}
			if id = tree.Root(); id == "" {
				return store.Session{}, &contract.Error{Code: contract.ErrNotFound, Message: "room has no open root"}
			}
		}
	default:
		return store.Session{}, forbidden()
	}
	session, err := s.store.Session(ctx, id)
	if err != nil {
		return store.Session{}, failure(err)
	}
	if _, open, err := s.store.OpenSession(ctx, id); err != nil {
		return store.Session{}, failure(err)
	} else if !open {
		return store.Session{}, &contract.Error{Code: contract.ErrSessionClosing, Message: "session is closed"}
	}
	return session, nil
}

func (s *Service) PrTrack(ctx context.Context, caller contract.Principal, req contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error) {
	owner, e := s.prTracker(ctx, caller, req)
	if e != nil {
		return contract.PrTrackResponse{}, e
	}
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.PrTrackResponse{}, e
	}
	if s.pulls == nil {
		return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "GitHub is not connected"}
	}
	// This is the one PR call a box can spend quota on.
	if reset, low := s.pulls.Low(); low && caller.Kind == contract.PrincipalSession {
		return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrGitHubQuota, Message: fmt.Sprintf("GitHub quota is low; try again after %s", reset.UTC().Format(time.RFC3339))}
	}
	pull, err := s.pulls.Pull(ctx, repo, req.Number)
	if errors.Is(err, prs.ErrNotFound) {
		return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "GitHub has no such pull request"}
	}
	if err != nil {
		return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "GitHub did not answer: " + err.Error()}
	}
	if caller.Kind == contract.PrincipalSession {
		repos, err := s.store.RoomRepos(ctx, owner.Room)
		if err != nil {
			return contract.PrTrackResponse{}, failure(err)
		}
		listed := func(item contract.RoomRepo) bool { return item.Repo == pull.Repo || item.Repo == pull.HeadRepo }
		if !slices.ContainsFunc(repos, listed) {
			return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "neither repository of this pull request is in your room; the owner can track it for you"}
		}
	}
	row := store.TrackedPR{HeadRepo: pull.HeadRepo, PrSummary: contract.PrSummary{Repo: pull.Repo, Number: req.Number, URL: pull.URL, Title: pull.Title, Author: pull.Author, Session: owner.ID, Draft: pull.Draft, State: pull.State, Head: pull.Head}}
	if err := s.store.TrackPR(ctx, row); err != nil {
		return contract.PrTrackResponse{}, failure(err)
	}
	s.resetPRPoll(pull.Repo)
	stored, e := s.trackedPR(ctx, caller, pull.Repo, req.Number)
	if e != nil {
		return contract.PrTrackResponse{}, e
	}
	return contract.PrTrackResponse{PR: prFor(caller, stored, s.prViewer())}, nil
}

// HookTrack tracks a PR the way the named session would for itself, or root with no name, so a Run gets no reach that
// the session lacks: the room must list the PR's repository, and tracking grants no push right.
func (s *Service) HookTrack(ctx context.Context, id contract.RoomID, name string, req contract.PrTrackRequest) (contract.PrTrackResponse, *contract.Error) {
	if id == "" {
		return contract.PrTrackResponse{}, invalid("a machine run has no room to track for")
	}
	tree, err := s.store.RoomTree(ctx, id)
	if err != nil {
		return contract.PrTrackResponse{}, failure(err)
	}
	session := tree.Root()
	if name = strings.TrimPrefix(name, "@"); name != "" {
		named, err := s.store.SessionByName(ctx, id, name)
		if errors.Is(err, store.ErrNotFound) {
			return contract.PrTrackResponse{}, invalid("unknown session @" + name)
		}
		if err != nil {
			return contract.PrTrackResponse{}, failure(err)
		}
		session = named.ID
	}
	if session == "" {
		return contract.PrTrackResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "room has no open root"}
	}
	return s.PrTrack(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, req)
}

// PrUntrack is for the owner, the session that owns the PR, and that session's ancestors.
func (s *Service) PrUntrack(ctx context.Context, caller contract.Principal, req contract.PrUntrackRequest) (contract.PrUntrackResponse, *contract.Error) {
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.PrUntrackResponse{}, e
	}
	pr, e := s.trackedPR(ctx, caller, repo, req.Number)
	if e != nil {
		return contract.PrUntrackResponse{}, e
	}
	if caller.Kind == contract.PrincipalSession && caller.Session != pr.Session {
		tree, err := s.store.RoomTree(ctx, pr.Room)
		if err != nil {
			return contract.PrUntrackResponse{}, failure(err)
		}
		if !tree.CanManage(caller, pr.Session) {
			return contract.PrUntrackResponse{}, forbidden()
		}
	}
	if err := s.store.UntrackPR(ctx, repo, req.Number); err != nil {
		return contract.PrUntrackResponse{}, failure(err)
	}
	return contract.PrUntrackResponse{}, nil
}
