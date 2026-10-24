package goals

import (
	"fmt"
	"strings"

	"dyna-agent/internal/room"
)

// AddWithIssues uses the same goal creation path as Add, then links room
// issues to the new goal. With no text, one linked issue supplies the title.
func (s *Store) AddWithIssues(roomID, by, actor string, in Input, fixes []int) (Goal, error) {
	links, err := s.issueLinks(roomID, fixes)
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
	return g, s.linkIssues(roomID, g.ID, actor, links)
}

// LinkIssues links existing open goals and room issues without closing them.
// A finished goal remains stored on the issue, but only an open goal makes the
// issue in progress at read time.
func (s *Store) LinkIssues(roomID, goalID, actor string, fixes []int) error {
	if len(fixes) == 0 {
		return nil
	}
	g, err := s.Get(roomID, goalID)
	if err != nil {
		return err
	}
	if !g.Open() {
		return fmt.Errorf("goal %s is not open", goalID)
	}
	links, err := s.issueLinks(roomID, fixes)
	if err != nil {
		return err
	}
	return s.linkIssues(roomID, goalID, actor, links)
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
		if issue.State != "open" && issue.State != "goal" {
			return nil, fmt.Errorf("issue D-%d is already %s", number, issue.State)
		}
		links = append(links, issue)
	}
	return links, nil
}

func (s *Store) linkIssues(roomID, goalID, actor string, links []room.Issue) error {
	if actor == "" && len(links) > 0 {
		rec, err := s.Issues.Resolve(roomID)
		if err != nil {
			return err
		}
		actor = rec.Name
	}
	for _, issue := range links {
		if _, err := s.Issues.LinkIssueGoal(roomID, issue.Label(), goalID, actor); err != nil {
			return fmt.Errorf("link %s to %s: %w", issue.Label(), goalID, err)
		}
	}
	return nil
}
