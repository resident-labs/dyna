package roomsvc

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

func (s *Service) Issues(roomRef string, all bool) ([]room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	issues, err := s.rooms().Issues(r.ID, true)
	if err != nil {
		return nil, err
	}
	openGoals, err := s.goalService().Store.ListOpen(r.ID)
	if err != nil {
		return nil, err
	}
	active := make(map[string]goals.Goal, len(openGoals))
	for _, goal := range openGoals {
		active[goal.ID] = goal
	}
	var fixes map[int]int
	if s.PRs != nil {
		fixes, err = s.PRs.IssueFixes(r.ID)
		if err != nil {
			return nil, err
		}
	}
	sessions, _ := s.sessions().ListSnapshot(true)
	names := make(map[string]string, len(sessions))
	for _, member := range sessions {
		names[member.ID] = member.Name
	}
	out := issues[:0]
	for _, issue := range issues {
		if goal, ok := active[issue.GoalID]; ok {
			issue.InProgress, issue.GoalShared = true, goal.Shared
			holders := []string{}
			for _, holder := range goal.Holders {
				if name := names[holder]; name != "" {
					holders = append(holders, name)
				}
			}
			issue.GoalHolder = strings.Join(holders, ", ")
		}
		if pr := fixes[issue.Number]; pr > 0 {
			issue.FixPR, issue.InProgress = pr, true
		}
		// The default is unhandled work. Closed goals leave their historical
		// goal ID, but do not keep the issue in progress.
		if !all && (issue.InProgress || issue.State != room.IssueStateOpen && issue.State != room.IssueStateGoal) {
			continue
		}
		out = append(out, issue)
	}
	return out, nil
}

// Issue mutations use the same triage policy as the CLI, with the actor
// supplied by the engine instead of the engine process's owner identity.
func (s *Service) CloseIssue(ctx context.Context, roomRef, id, reason string) (room.Issue, error) {
	return s.triageIssueAsCaller(ctx, roomRef, id, "close", reason)
}
func (s *Service) IssueGoal(ctx context.Context, roomRef, id string) (room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	actor, err := authority.ActorIn(ctx, r.ID)
	if err != nil {
		return room.Issue{}, err
	}
	if err := authority.Check("ChangeIssue", actor, nil); err != nil {
		return room.Issue{}, err
	}
	issue, err := s.rooms().Issue(r.ID, id)
	if err != nil {
		return room.Issue{}, err
	}
	if issue.State != room.IssueStateOpen {
		return room.Issue{}, fmt.Errorf("issue %s is already %s", issue.Label(), issue.State)
	}
	post, err := s.rooms().Message(r.ID, issue.ID)
	if err != nil {
		return room.Issue{}, err
	}
	in := goals.Input{Text: issue.Title, Detail: issueDetail(issue, issueText(post))}
	// The whole room holds a goal that starts from an issue: the issue names
	// no holder. The goal is the one dyna goal creates, and the issue is its
	// first link. The claim takes only an issue still open, so two assignments
	// of one issue start one goal: the loser's goal is deleted.
	req := goalStart{In: in, Selectors: []string{"@room"}, Fixes: []int{issue.Number}, Claim: goals.ClaimOpen}
	if _, err = s.startGoal(ctx, r.ID, req); err != nil {
		return room.Issue{}, err
	}
	return s.rooms().Issue(r.ID, id)
}

func (s *Service) triageIssueAsCaller(ctx context.Context, roomRef, id, action, value string) (room.Issue, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	actor, err := authority.ActorIn(ctx, rec.ID)
	if err != nil {
		return room.Issue{}, err
	}
	if err := authority.Check("ChangeIssue", actor, nil); err != nil {
		return room.Issue{}, err
	}
	by := rec.Name
	if actor.Role != authority.Owner {
		info, err := s.sessions().Inspect(actor.Session)
		if err != nil {
			return room.Issue{}, err
		}
		by = info.Address()
	}
	return s.TriageIssue(ctx, rec.ID, id, action, value, by)
}

func (s *Service) ExportIssue(ctx context.Context, roomRef, id string) (room.Issue, error) {
	return s.triageIssueAsCaller(ctx, roomRef, id, "export", "")
}

// TriageIssue takes the actor derived by the CLI, or empty for the owner API.
// It closes or exports an issue; starting a goal from one goes through
// IssueGoal and the goal creation path of dyna goal.
func (s *Service) TriageIssue(ctx context.Context, roomRef, id, action, value, by string) (room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	if by == "" {
		by = r.Name
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.rooms().ChangeIssue(r.ID, id, by, func(i *room.Issue) error {
		switch action {
		case "close":
			i.State = room.IssueStateClosed
			i.Reason = strings.TrimSpace(value)
		case "export":
			if ghclient.Access(ctx, r) != ghclient.ReadWrite {
				return fmt.Errorf("room %s has read-only GitHub access", r.Name)
			}
			if r.Repo == "" {
				return fmt.Errorf("room %s has no repository", r.Name)
			}
			post, err := s.rooms().Message(r.ID, i.ID)
			if err != nil {
				return err
			}
			repository, err := repoid.Resolve(ctx, r.Repo)
			if err != nil {
				return fmt.Errorf("resolve issue export repository: %w", err)
			}
			parts := strings.Split(repository.ID, "/")
			if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return fmt.Errorf("issue export needs a hosted repository")
			}
			ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.rooms()), r.ID)
			ctx = ghclient.WithRepository(ctx, parts[0], parts[1]+"/"+parts[2])
			cmd := ghclient.Command(ctx, "", r.Repo, "issue", "create", "--repo", repository.ID, "--title", i.Title, "--body", issueDetail(*i, issueText(post)))
			var out, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err = ghclient.Run(ctx, cmd); err != nil {
				return fmt.Errorf("create GitHub issue: %w: %s", err, strings.TrimSpace(stderr.String()))
			}
			raw := strings.TrimSpace(out.String())
			u, err := url.Parse(raw)
			if err != nil || u.Scheme != "https" || u.Host == "" || !strings.Contains(u.Path, "/issues/") {
				return fmt.Errorf("gh issue create returned an invalid issue URL")
			}
			i.State, i.GitHubURL = room.IssueStateExported, raw
		default:
			return fmt.Errorf("unknown issue action %q", action)
		}
		return nil
	})
}

// issueText is the issue's own body: the note it was filed with, without the
// event's one-line summary. An issue filed before the typed events still
// carries its note in the post text.
func issueText(msg room.Message) string {
	if ev, ok := msg.Issue(); ok && ev.Detail != "" {
		return ev.Detail
	}
	return msg.Text
}

func issueDetail(i room.Issue, text string) string {
	detail := fmt.Sprintf("Local issue %s\n\n%s", i.Label(), text)
	if i.File != "" {
		detail += "\n\nLocation: " + i.File
		if i.Line > 0 {
			detail += fmt.Sprintf(":%d", i.Line)
		}
	}
	return detail
}

// scanIssues shares the room watcher signature map with the other small files.
func (s *Service) scanIssues(roomID string, signatures map[string]string, first bool, emit func(Event)) {
	sig := ""
	if rev, err := s.rooms().IssuesRevision(roomID); err == nil {
		sig = fmt.Sprint(rev)
	}
	key := "issues:" + roomID
	if sig != signatures[key] {
		signatures[key] = sig
		if !first {
			emit(Event{Kind: "issues", RoomID: roomID})
		}
	}
}
