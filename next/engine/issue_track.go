package engine

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

// issueFor returns an issue for this caller. Free text on an issue by anyone but the owner's account reaches a session wrapped (decision 13).
func issueFor(caller contract.Principal, i store.TrackedIssue, viewer string) contract.IssueSummary {
	summary := i.IssueSummary
	if caller.Kind != contract.PrincipalSession || summary.Author == viewer {
		return summary
	}
	summary.Title = room.WrapExternal(summary.Author, summary.Title)
	if summary.Milestone != "" {
		summary.Milestone = room.WrapExternal(summary.Author, summary.Milestone)
	}
	if len(summary.Labels) > 0 {
		summary.Labels = []string{room.WrapExternal(summary.Author, strings.Join(summary.Labels, ", "))}
	}
	return summary
}

// trackedIssue reads an issue the caller may see: the owner sees all, and a session those of its room.
func (s *Service) trackedIssue(ctx context.Context, caller contract.Principal, repo contract.RepoName, number int) (store.TrackedIssue, *contract.Error) {
	issue, err := s.store.Issue(ctx, repo, number)
	if errors.Is(err, store.ErrNotFound) {
		return issue, &contract.Error{Code: contract.ErrNotFound, Message: "issue is not tracked"}
	}
	if err != nil {
		return issue, failure(err)
	}
	return issue, s.roomAccess(ctx, caller, issue.Room)
}

func (s *Service) IssueList(ctx context.Context, caller contract.Principal, req contract.IssueListRequest) (contract.IssueListResponse, *contract.Error) {
	target, e := s.listRoom(ctx, caller, req.Room)
	if e != nil {
		return contract.IssueListResponse{}, e
	}
	rows, err := s.store.RoomIssues(ctx, target, req.All)
	if err != nil {
		return contract.IssueListResponse{}, failure(err)
	}
	result := contract.IssueListResponse{Issues: make([]contract.IssueSummary, 0, len(rows))}
	viewer := s.prViewer()
	for _, row := range rows {
		result.Issues = append(result.Issues, issueFor(caller, row, viewer))
	}
	return result, nil
}

func (s *Service) IssueStatus(ctx context.Context, caller contract.Principal, req contract.IssueStatusRequest) (contract.IssueStatusResponse, *contract.Error) {
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.IssueStatusResponse{}, e
	}
	issue, e := s.trackedIssue(ctx, caller, repo, req.Number)
	if e != nil {
		return contract.IssueStatusResponse{}, e
	}
	return contract.IssueStatusResponse{Issue: issueFor(caller, issue, s.prViewer())}, nil
}

// IssueTrack has PrTrack's rules. An issue has one repository, so the room must list that one. Its read is one GraphQL
// query, which costs quota where a PR's ETag read may not, so tracking an issue the session already owns reads nothing.
func (s *Service) IssueTrack(ctx context.Context, caller contract.Principal, req contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error) {
	owner, e := s.prTracker(ctx, caller, contract.PrTrackRequest(req))
	if e != nil {
		return contract.IssueTrackResponse{}, e
	}
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.IssueTrackResponse{}, e
	}
	if held, err := s.store.Issue(ctx, repo, req.Number); err == nil && held.Session == owner.ID && held.State == contract.IssueOpen {
		return contract.IssueTrackResponse{Issue: issueFor(caller, held, s.prViewer())}, nil
	}
	if e := s.trackQuota(caller); e != nil {
		return contract.IssueTrackResponse{}, e
	}
	repo, seen, err := s.pulls.Issue(ctx, repo, req.Number)
	if errors.Is(err, prs.ErrNotFound) {
		return contract.IssueTrackResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "GitHub has no such issue; a pull request is tracked with pr track"}
	}
	if err != nil {
		return contract.IssueTrackResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: "GitHub did not answer: " + err.Error()}
	}
	if caller.Kind == contract.PrincipalSession {
		repos, err := s.store.RoomRepos(ctx, owner.Room)
		if err != nil {
			return contract.IssueTrackResponse{}, failure(err)
		}
		if !slices.ContainsFunc(repos, func(item contract.RoomRepo) bool { return item.Repo == repo }) {
			return contract.IssueTrackResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "the repository of this issue is not in your room; the owner can track it for you"}
		}
	}
	row := store.TrackedIssue{IssueSummary: contract.IssueSummary{Repo: repo, Number: req.Number, URL: seen.URL, Title: seen.Meta.Title, Author: seen.Author, Session: owner.ID, State: seen.State}}
	if err := s.store.TrackIssue(ctx, row); err != nil {
		return contract.IssueTrackResponse{}, failure(err)
	}
	s.resetPRPoll(repo)
	stored, e := s.trackedIssue(ctx, caller, repo, req.Number)
	if e != nil {
		return contract.IssueTrackResponse{}, e
	}
	return contract.IssueTrackResponse{Issue: issueFor(caller, stored, s.prViewer())}, nil
}

// HookTrackIssue tracks an issue the way the named session would for itself, or root with no name, as HookTrack does a PR.
func (s *Service) HookTrackIssue(ctx context.Context, id contract.RoomID, name string, req contract.IssueTrackRequest) (contract.IssueTrackResponse, *contract.Error) {
	caller, e := s.hookSession(ctx, id, name)
	if e != nil {
		return contract.IssueTrackResponse{}, e
	}
	return s.IssueTrack(ctx, caller, req)
}

// IssueUntrack is for the owner, the session that owns the issue, and that session's ancestors.
func (s *Service) IssueUntrack(ctx context.Context, caller contract.Principal, req contract.IssueUntrackRequest) (contract.IssueUntrackResponse, *contract.Error) {
	repo, e := s.prRepo(ctx, caller, req.Repo, req.Number)
	if e != nil {
		return contract.IssueUntrackResponse{}, e
	}
	issue, e := s.trackedIssue(ctx, caller, repo, req.Number)
	if e != nil {
		return contract.IssueUntrackResponse{}, e
	}
	if e := s.mayUntrack(ctx, caller, issue.Room, issue.Session); e != nil {
		return contract.IssueUntrackResponse{}, e
	}
	if err := s.store.UntrackIssue(ctx, repo, req.Number); err != nil {
		return contract.IssueUntrackResponse{}, failure(err)
	}
	return contract.IssueUntrackResponse{}, nil
}
