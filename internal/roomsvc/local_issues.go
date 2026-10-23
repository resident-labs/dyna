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
	"dyna-agent/internal/notify"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

func (s *Service) Issues(roomRef string, all bool) ([]room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	issues, err := s.rooms().Issues(r.ID, all)
	if err != nil {
		return nil, err
	}
	// The issue card shows "Fix in #N" while a linked PR is open.
	if s.PRs != nil {
		fixes, err := s.PRs.IssueFixes(r.ID)
		if err != nil {
			return nil, err
		}
		for i := range issues {
			issues[i].FixPR = fixes[issues[i].Number]
		}
	}
	return issues, nil
}

// Issue mutations use the same triage policy as the CLI, with the actor
// supplied by the engine instead of the engine process's owner identity.
func (s *Service) CloseIssue(ctx context.Context, roomRef, id, reason string) (room.Issue, error) {
	return s.triageIssueAsCaller(ctx, roomRef, id, "close", reason)
}
func (s *Service) IssueGoal(ctx context.Context, roomRef, id, holder string) (room.Issue, error) {
	return s.triageIssueAsCaller(ctx, roomRef, id, "goal", holder)
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
// A goal holder is an optional @name selector. Without one the goal is shared.
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
			i.State = "closed"
			i.Reason = strings.TrimSpace(value)
		case "goal":
			in := goals.Input{Text: i.Title, Shared: true}
			post, err := s.rooms().Message(r.ID, i.ID)
			if err != nil {
				return err
			}
			in.Detail = issueDetail(*i, post.Text)
			setter := "owner"
			if by != r.Name {
				members, err := s.sessions().ListSnapshot(false)
				if err != nil {
					return err
				}
				for _, m := range members {
					if m.RoomID == r.ID && m.Address() == by {
						setter = m.ID
						break
					}
				}
				if setter == "owner" {
					return fmt.Errorf("root session is not available in this room")
				}
			}
			if value != "" {
				if !strings.HasPrefix(value, "@") || strings.Contains(value, ":") {
					return fmt.Errorf("--for needs @name in this room")
				}
				sessions, err := s.Notify.ResolveSessions(notify.Sender{Kind: notify.SenderOwner, Room: r.Name}, []string{value})
				if err != nil {
					return err
				}
				in.Shared = false
				for _, m := range sessions {
					if m.RoomID != r.ID {
						return fmt.Errorf("goal holder must belong to this room")
					}
					in.Holders = append(in.Holders, m.ID)
				}
				if err = s.validGoalHolders(r.ID, in); err != nil {
					return err
				}
			}
			g, err := s.goalService().Store.Add(r.ID, setter, in)
			if err != nil {
				return err
			}
			i.State, i.GoalID = "goal", g.ID
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
			cmd := ghclient.Command(ctx, "", r.Repo, "issue", "create", "--repo", repository.ID, "--title", i.Title, "--body", issueDetail(*i, post.Text))
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
			i.State, i.GitHubURL = "exported", raw
		default:
			return fmt.Errorf("unknown issue action %q", action)
		}
		return nil
	})
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
