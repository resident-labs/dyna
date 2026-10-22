package roomsvc

import (
	"context"
	"fmt"
	"strings"

	"dyna-agent/internal/pr/state"
	prview "dyna-agent/internal/pr/view"
)

// StartIssueWork is the shared action for the issue list and issue page.
func (s *Service) StartIssueWork(ctx context.Context, roomRef string, number int) (SpawnResult, error) {
	s.issueStartMu.Lock()
	defer s.issueStartMu.Unlock()
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return SpawnResult{}, err
	}
	issue, err := s.PRs.GitHubView(rec.ID, "issue", number)
	if err != nil {
		return SpawnResult{}, err
	}
	if issue.WorkingSessionLive {
		member, err := s.memberByID(issue.WorkingSession)
		return SpawnResult{Member: member}, err
	}
	if !strings.EqualFold(issue.State, "open") {
		return SpawnResult{}, fmt.Errorf("issue #%d is closed", number)
	}
	repo, err := prview.IssueRepository(issue.URL)
	if err != nil {
		return SpawnResult{}, err
	}
	profiles, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return SpawnResult{}, err
	}
	profile, ok := profiles.SubagentDefaultProfile()
	if !ok {
		return SpawnResult{}, fmt.Errorf("no enabled default profile is available")
	}
	brief := fmt.Sprintf("Work on issue #%d: %s\n%s\n\n%s", number, issue.Title, issue.URL, issue.Body)
	result, err := s.StartSession(ctx, rec.ID, "/"+profile.Name+" "+brief)
	if err != nil {
		return result, err
	}
	err = s.PRs.Watcher().Store.SaveIssueWork(state.IssueWork{RoomID: rec.ID, Repo: repo, Number: number, Session: result.Member.ID})
	if err != nil {
		return result, fmt.Errorf("session %s started, but saving its issue failed: %w", result.Member.ID, err)
	}
	return result, nil
}
