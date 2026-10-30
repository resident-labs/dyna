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

// TrackedIssues is the GitHub half of the Issues tab's Tracked view.
type TrackedIssues struct {
	Items []GitHubItem `json:"items"`
	// Unread counts issues with live room work that GitHub could not describe;
	// the view offers Retry for them.
	Unread int `json:"unread"`
}

// TrackedIssues lists the issues the room watches (dyna git issue watch), with
// the facts the last read saved, and the GitHub issues a live room session
// works on. Watches come from dyna.db. Worked issues come from one GitHub query
// per host, so they appear whatever page the GitHub list shows.
func (s *Service) TrackedIssues(roomRef string) (TrackedIssues, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return TrackedIssues{}, err
	}
	watches, err := issuewatch.Store{DataDir: s.dataDir()}.Room(context.Background(), rec.ID)
	if err != nil {
		return TrackedIssues{}, err
	}
	out := TrackedIssues{Items: make([]GitHubItem, 0, len(watches))}
	watched := map[string]bool{}
	for _, w := range watches {
		// A watch saved before its first read has no update time yet.
		updated, _ := time.Parse(time.RFC3339, w.Updated)
		out.Items = append(out.Items, GitHubItem{Number: w.Number, Title: w.Title, State: strings.ToUpper(w.State), Labels: []GitHubLabel{},
			UpdatedAt: updated, URL: w.URL()})
		watched[state.IssueWorkKey(rec.ID, w.Host+"/"+w.Repo, w.Number)] = true
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return TrackedIssues{}, err
	}
	var work []state.IssueWork
	for key, w := range st.IssueWork {
		if w.RoomID != rec.ID || watched[key] || w.Session == "" {
			continue
		}
		info, err := s.Notify.Sessions.Store.Inspect(w.Session)
		if err == nil && info.RoomID == rec.ID && info.Status != subagent.StatusClosed && info.Status != subagent.StatusDormant {
			work = append(work, w)
		}
	}
	if len(work) > 0 {
		worked, err := s.workedIssues(roomRef, work)
		if err != nil {
			out.Unread = len(work)
		}
		out.Items = append(out.Items, worked...)
	}
	return out, s.issueWork(rec.ID, out.Items)
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
