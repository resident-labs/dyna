package goals

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dyna-agent/internal/room"
)

// A Claim is the room issue state a goal requires when it links the issue.
// The link store checks it in the same locked step that writes the link, so a
// second goal cannot take an issue another goal has just claimed.
type Claim int

const (
	// ClaimAny links an issue in the state the caller prepared: one still open,
	// or one that already names a goal, which is the deliberate replacement of
	// finished work.
	ClaimAny Claim = iota
	// ClaimOpen links only an issue that is still open, so two requests that
	// both read the issue as open start one goal, not two.
	ClaimOpen
)

// state is the issue state claim requires, or empty for the prepared state.
func (c Claim) state(prepared room.IssueState) room.IssueState {
	if c == ClaimOpen {
		return room.IssueStateOpen
	}
	return prepared
}

// AddWithIssues uses the same goal creation path as Add, then links room
// issues to the new goal. With no text, one linked issue supplies the title.
// A link the claim refuses deletes the new goal, so a losing assignment leaves
// no goal behind. The caller on ctx decides what it may link; the actor address
// only credits the links.
func (s *Store) AddWithIssues(ctx context.Context, roomID, by, actor string, in Input, fixes []int, claim Claim) (Goal, error) {
	links, err := s.PrepareIssueLinks(ctx, roomID, fixes)
	if err != nil {
		return Goal{}, err
	}
	if strings.TrimSpace(in.Text) == "" && len(links) > 0 {
		if len(links) != 1 {
			return Goal{}, fmt.Errorf("goal text is required when --fixes names more than one issue")
		}
		in.Text = links[0].Title
	}
	g, err := s.Add(roomID, by, in)
	if err != nil {
		return g, err
	}
	if err := s.LinkPreparedIssues(ctx, roomID, g.ID, actor, links, claim); err != nil {
		return g, errors.Join(err, s.deleteGoal(roomID, g.ID))
	}
	return g, nil
}

// PrepareIssueLinks validates permission and every issue before any goal,
// holder or item changes. The returned snapshots also restore partial work if
// a later step fails.
func (s *Store) PrepareIssueLinks(ctx context.Context, roomID string, fixes []int) ([]room.Issue, error) {
	links, err := s.issueLinks(roomID, fixes)
	if err != nil || len(links) == 0 {
		return links, err
	}
	if err := s.Issues.CheckIssueGoalLink(ctx, roomID); err != nil {
		return nil, err
	}
	return links, nil
}

// LinkPreparedIssues applies snapshots from PrepareIssueLinks. A failure
// restores any links already changed by this call.
func (s *Store) LinkPreparedIssues(ctx context.Context, roomID, goalID, by string, links []room.Issue, claim Claim) error {
	for i, issue := range links {
		linked, err := s.Issues.LinkIssueGoal(ctx, roomID, issue.Label(), goalID, by, claim.state(issue.State))
		if err != nil {
			restore := links[:i]
			if linked.ID == issue.ID && linked.State == room.IssueStateGoal && linked.GoalID == goalID {
				restore = links[:i+1]
			}
			return errors.Join(fmt.Errorf("link %s to %s: %w", issue.Label(), goalID, err), s.RestoreIssueLinks(ctx, roomID, by, restore))
		}
	}
	return nil
}

// RestoreIssueLinks puts prepared issue snapshots back after a later goal or
// session step fails.
func (s *Store) RestoreIssueLinks(ctx context.Context, roomID, by string, links []room.Issue) error {
	var errs []error
	for _, issue := range links {
		if _, err := s.Issues.RestoreIssueGoal(ctx, roomID, issue.Label(), issue, by); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", issue.Label(), err))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) issueLinks(roomID string, fixes []int) ([]room.Issue, error) {
	if len(fixes) == 0 {
		return nil, nil
	}
	if s.Issues == nil {
		return nil, fmt.Errorf("room issue links are unavailable")
	}
	all, err := s.Issues.Issues(roomID, true)
	if err != nil {
		return nil, err
	}
	byNumber := make(map[int]room.Issue, len(all))
	for _, issue := range all {
		byNumber[issue.Number] = issue
	}
	seen := map[int]bool{}
	links := make([]room.Issue, 0, len(fixes))
	for _, number := range fixes {
		if number < 1 {
			return nil, fmt.Errorf("room issue numbers must be positive")
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		issue, ok := byNumber[number]
		if !ok {
			return nil, fmt.Errorf("issue D-%d not found", number)
		}
		if issue.State != room.IssueStateOpen && issue.State != room.IssueStateGoal {
			return nil, fmt.Errorf("issue D-%d is already %s", number, issue.State)
		}
		links = append(links, issue)
	}
	return links, nil
}

// deleteGoal removes only a new goal whose first issue link failed.
func (s *Store) deleteGoal(roomID, goalID string) error {
	where, args := named(roomID, goalID)
	return s.updateTx(func(_ *wake, st *State) error {
		delete(st.Room(roomID).Goals, goalID)
		return nil
	}, where, args...)
}
