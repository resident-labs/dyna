package view

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"dyna-agent/internal/pr/issuewatch"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
)

type GitHubLinkedPR struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	IsDraft bool   `json:"isDraft"`
	URL     string `json:"url"`
}

// IssueRepository uses the issue URL so facts also distinguish GitHub hosts.
func IssueRepository(issueURL string) (string, error) {
	u, err := url.Parse(issueURL)
	if err != nil {
		return "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Host == "" || len(parts) != 4 || parts[2] != "issues" {
		return "", fmt.Errorf("invalid issue URL %q", issueURL)
	}
	return strings.ToLower(u.Host + "/" + parts[0] + "/" + parts[1]), nil
}

// IssueWatches lists the issues the room watches (dyna git issue watch) as
// issue rows, with the facts the last read saved and the room's work facts.
// It reads only dyna.db, so the Issues tab's Tracked view needs no GitHub call
// to list them.
func (s *Service) IssueWatches(roomRef string) ([]GitHubItem, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	watches, err := issuewatch.Store{DataDir: s.dataDir()}.Room(context.Background(), rec.ID)
	if err != nil {
		return nil, err
	}
	items := make([]GitHubItem, 0, len(watches))
	for _, w := range watches {
		// A watch saved before its first read has no update time yet.
		updated, _ := time.Parse(time.RFC3339, w.Updated)
		items = append(items, GitHubItem{Number: w.Number, Title: w.Title, State: strings.ToUpper(w.State), Labels: []GitHubLabel{},
			UpdatedAt: updated, URL: fmt.Sprintf("https://%s/%s/issues/%d", w.Host, w.Repo, w.Number)})
	}
	return items, s.issueWork(rec.ID, items)
}

// IssueGroup gives closed issues priority, followed by open PRs and live sessions.
// An empty group leaves older closed issues available only in Closed and All.
func IssueGroup(it GitHubItem, now time.Time) string {
	if strings.EqualFold(it.State, "closed") {
		if it.ClosedAt != nil && it.ClosedAt.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
			return "closed-today"
		}
		return ""
	}
	for _, pr := range it.LinkedPRs {
		if strings.EqualFold(pr.State, "open") {
			return "has-pr"
		}
	}
	if it.WorkingSessionLive {
		return "working"
	}
	return "needs-owner"
}

func (s *Service) issueWork(roomRef string, items []GitHubItem) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return err
	}
	now := time.Now()
	for i := range items {
		it := &items[i]
		repo, err := IssueRepository(it.URL)
		if err != nil {
			return err
		}
		work := st.IssueWork[state.IssueWorkKey(rec.ID, repo, it.Number)]
		it.WorkingSession = work.Session
		if work.Session != "" {
			info, err := s.Notify.Sessions.Store.Inspect(work.Session)
			if err == nil && info.RoomID == rec.ID {
				it.WorkingSessionName = info.EffectiveName()
				it.WorkingSessionLive = info.Status != subagent.StatusClosed && info.Status != subagent.StatusDormant
			}
		}
		it.Group = IssueGroup(*it, now)
	}
	return nil
}
