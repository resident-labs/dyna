package watch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// closeFixedIssues closes the room and GitHub issues that a merged PR names.
// Each merged PR closes its list once; a PR that closed without merging
// closes nothing. A close that fails leaves that issue open and returns an
// error so the merge report shows it.
func (s *Service) closeFixedIssues(ctx context.Context, r Record, mergeCommit string) error {
	if r.State != state.PRMerged || len(r.Fixes) == 0 || s.Notify == nil || s.Notify.Rooms == nil {
		return nil
	}
	rec, err := s.Notify.Rooms.Resolve(r.RoomID)
	if err != nil {
		return fmt.Errorf("close issues fixed by %s: %w", changeLabel(r), err)
	}
	// The reason is the fact only. The short commit is the close's origin, which
	// the card shows beside the PR, so the reason never repeats it.
	reason := "Fixed by #" + strconv.Itoa(r.Number)
	var failures []error
	for _, fix := range r.Fixes {
		switch fix.Kind {
		case state.IssueKindDyna:
			if err := s.closeRoomIssue(ctx, rec, fix, reason, r.Number, mergeCommit); err != nil {
				failures = append(failures, err)
			}
		case state.IssueKindGitHub:
			if err := s.closeGitHubIssue(ctx, r, fix, mergeCommit); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// closeRoomIssue closes one open issue of the PR's room. An issue that is
// already closed, or gone, is not an error: the same close may have run
// before, or a person closed it first.
func (s *Service) closeRoomIssue(ctx context.Context, rec room.Record, fix state.IssueRef, reason string, pr int, commit string) error {
	open, err := s.Notify.Rooms.Issues(rec.ID, true)
	if err != nil {
		return fmt.Errorf("read room issues: %w", err)
	}
	id := ""
	for _, issue := range open {
		if issue.Number == fix.Number && (issue.State == room.IssueStateOpen || issue.State == room.IssueStateGoal) {
			id = issue.Label()
			break
		}
	}
	if id == "" {
		return nil
	}
	// An automatic close is the engine's own triage, so it acts as the room's
	// root seat. The address below stays attribution: the event shows the root
	// seat, and no address decides what the close may do.
	ctx = authority.WithActor(ctx, authority.EngineRoomActor(rec.ID))
	_, err = s.Notify.Rooms.CloseIssue(ctx, rec.ID, id, rec.Name+":"+room.RootAgent, reason, room.CloseOrigin{PR: pr, Commit: commit})
	if err != nil && strings.Contains(err.Error(), "is already ") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("close %s: %w", fix.Label(), err)
	}
	return nil
}

// closeGitHubIssue closes one open issue of the PR's repository. GitHub may
// have closed it natively from a "Fixes #N" line, so a closed issue is done.
func (s *Service) closeGitHubIssue(ctx context.Context, r Record, fix state.IssueRef, commit string) error {
	repository := r.Repo
	if repository == "" {
		return fmt.Errorf("close %s: the PR has no repository", fix.Label())
	}
	number := strconv.Itoa(fix.Number)
	out, err := s.github(r.RoomID).command(ctx, "", false, "issue", "view", number, "--repo", repository, "--json", "state", "--jq", ".state")
	if err != nil {
		return fmt.Errorf("read %s: %w", fix.Label(), err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(out)), "open") {
		return nil
	}
	comment := "Fixed by #" + strconv.Itoa(r.Number) + "."
	if commit != "" {
		comment = "Fixed by #" + strconv.Itoa(r.Number) + " in " + shortSha(commit) + "."
	}
	if _, err := s.github(r.RoomID).command(ctx, "", false, "issue", "close", number, "--repo", repository, "--reason", "completed", "--comment", comment); err != nil {
		return fmt.Errorf("close %s: %w", fix.Label(), err)
	}
	return nil
}

// shortSha is the commit prefix that people and the UI use.
func shortSha(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
