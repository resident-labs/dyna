package roomsvc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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
	if issues, err = s.issueState(r.ID, issues); err != nil {
		return nil, err
	}
	out := issues[:0]
	for _, issue := range issues {
		// The default is unhandled work. Closed goals leave their historical
		// goal ID, but do not keep the issue in progress.
		if !all && (issue.InProgress || issue.State != room.IssueStateOpen && issue.State != room.IssueStateGoal) {
			continue
		}
		out = append(out, issue)
	}
	return out, nil
}

// issueState fills the live state an issue does not store: whether an open
// goal holds it, who that goal's holders are, and which open PR fixes it. The
// list and the single-issue read share it, so both describe the same work.
func (s *Service) issueState(roomID string, issues []room.Issue) ([]room.Issue, error) {
	openGoals, err := s.goalService().Store.ListOpen(roomID)
	if err != nil {
		return nil, err
	}
	active := make(map[string]goals.Goal, len(openGoals))
	for _, goal := range openGoals {
		active[goal.ID] = goal
	}
	var fixes map[int]int
	if s.PRs != nil {
		fixes, err = s.PRs.IssueFixes(roomID)
		if err != nil {
			return nil, err
		}
	}
	// Only the holders of goals that hold these issues need a name. Listing
	// every session, closed ones included, read and parsed all their records.
	names := map[string]string{}
	for _, issue := range issues {
		for _, holder := range active[issue.GoalID].Holders {
			if _, seen := names[holder]; !seen {
				info, _ := s.sessions().Inspect(holder)
				names[holder] = info.Name
			}
		}
	}
	for i := range issues {
		issue := &issues[i]
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
	}
	return issues, nil
}

// Issue reads one issue by D-N, its compact spelling or the ID of the post it
// was filed with, with the note the filer gave it and the same live state the
// list shows.
func (s *Service) Issue(roomRef, id string) (room.Issue, string, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, "", err
	}
	i, err := s.rooms().Issue(r.ID, id)
	if err != nil {
		return room.Issue{}, "", err
	}
	one, err := s.issueState(r.ID, []room.Issue{i})
	if err != nil {
		return room.Issue{}, "", err
	}
	post, err := s.rooms().Message(r.ID, i.ID)
	if err != nil {
		return room.Issue{}, "", err
	}
	return one[0], strings.TrimSpace(issueText(post)), nil
}

// IssueNote is one note added to an issue after it was filed.
type IssueNote struct {
	By   string `json:"by"`
	At   int64  `json:"at"`
	Text string `json:"text"`
	// Attachments are the files saved with the note, so an attachment-only note
	// still shows where its evidence is.
	Attachments []IssueNoteAttachment `json:"attachments,omitempty"`
}

// IssueNoteAttachment names one file of a note and the path of its saved bytes.
type IssueNoteAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
	Path string `json:"path"`
}

// IssueNotes lists the notes added to an issue with dyna issue note, oldest
// first. The note the filer gave is not among them: Issue returns it.
func (s *Service) IssueNotes(roomRef, id string) ([]IssueNote, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return nil, err
	}
	i, err := s.rooms().Issue(r.ID, id)
	if err != nil {
		return nil, err
	}
	msgs, err := s.rooms().Messages(r.ID)
	if err != nil {
		return nil, err
	}
	var notes []IssueNote
	for _, msg := range msgs {
		ev, ok := msg.Issue()
		if !ok || ev.Type != room.IssueNoted || ev.IssueID != i.ID {
			continue
		}
		by := string(ev.Actor)
		if by == "" {
			by = msg.From
		}
		note := IssueNote{By: by, At: msg.TS, Text: strings.TrimSpace(ev.Detail)}
		for _, att := range msg.Attachments {
			path, err := s.rooms().AttachmentPath(r.ID, att.ID)
			if err != nil {
				return nil, err
			}
			note.Attachments = append(note.Attachments, IssueNoteAttachment{ID: att.ID, Name: att.Name, MIME: att.MIME, Size: att.Size, Path: path})
		}
		notes = append(notes, note)
	}
	return notes, nil
}

// Issue mutations use the same triage policy as the CLI, with the caller that
// the engine verified instead of the engine process's owner identity.
func (s *Service) CloseIssue(ctx context.Context, roomRef, id, reason string) (room.Issue, error) {
	return s.TriageIssue(ctx, roomRef, id, "close", reason, "")
}
func (s *Service) IssueGoal(ctx context.Context, roomRef, id string) (room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	actor, err := s.issueCaller(ctx, r.ID)
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
	if _, err = s.startGoal(authority.WithActor(ctx, actor), r.ID, req); err != nil {
		return room.Issue{}, err
	}
	return s.rooms().Issue(r.ID, id)
}

func (s *Service) ExportIssue(ctx context.Context, roomRef, id string) (room.Issue, error) {
	return s.TriageIssue(ctx, roomRef, id, "export", "", "")
}

// minCrossRoomNote is the shortest note an issue filed in another room
// accepts: the note must say what happened and how to reproduce it (D-401).
const minCrossRoomNote = 20

// FileIssue files a quiet issue for the caller on ctx. The caller's own room
// keeps the filing rules of D-111. Another room accepts only the owner or a
// room's root, and the note must explain the problem: a worker reports the
// problem to its own root instead (D-401). A caller with no room that is not
// the owner files nowhere.
func (s *Service) FileIssue(ctx context.Context, roomRef string, in room.IssueInput) (room.Issue, error) {
	r, err := s.rooms().ResolveQuietWrite(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	actor, err := s.storedActor(ctx)
	if err != nil {
		return room.Issue{}, err
	}
	by, err := s.issueAddress(actor, r)
	if err != nil {
		return room.Issue{}, err
	}
	in.Filer = by
	// The caller's own room: the stored room of a session or root seat, or
	// the room stamp of an owner process, which may carry the name. An owner
	// without a stamp has no room of its own, so no filing is cross-room for
	// it; every other caller needs a room to file from, so a workflow worker
	// or an unauthenticated actor files nowhere.
	own := actor.Room
	if actor.Role != authority.Owner && own == "" {
		return room.Issue{}, errors.New("this caller has no room to file from; only the owner files without one")
	}
	if own == "" || own == r.ID || own == r.Name {
		return s.rooms().FileIssue(r.ID, in)
	}
	if err := authority.Check("issue file cross-room", actor, nil); err != nil {
		return room.Issue{}, err
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Note)) < minCrossRoomNote {
		return room.Issue{}, fmt.Errorf("an issue in another room needs a note of at least %d characters that says what happened and how to reproduce it", minCrossRoomNote)
	}
	return s.rooms().FileQuietIssue(r.ID, in)
}

// storedActor is the caller behind an issue command. The engine verifies the
// callers that reach it; a session's own process states only its ID, so its
// stored role and room decide, as no address it can type ever does.
func (s *Service) storedActor(ctx context.Context) (authority.Actor, error) {
	actor := authority.Of(ctx)
	if _, verified := authority.ContextActor(ctx); !verified && actor.Role == authority.Session {
		stored, err := s.sessions().SessionActor(actor.Session)
		if err != nil {
			return authority.Actor{}, err
		}
		actor = stored
	}
	return actor, nil
}

// issueCaller is the caller of an issue change, confined to the room it acts
// in.
func (s *Service) issueCaller(ctx context.Context, roomID string) (authority.Actor, error) {
	actor, err := s.storedActor(ctx)
	if err != nil {
		return authority.Actor{}, err
	}
	return authority.ActorIn(authority.WithActor(ctx, actor), roomID)
}

// issueAddress is the address that credits an issue change: the room name for
// the owner, and the session's own address for a session or root seat.
func (s *Service) issueAddress(actor authority.Actor, r room.Record) (string, error) {
	if actor.Role == authority.Owner || actor.Session == "" {
		return r.Name, nil
	}
	info, err := s.sessions().Inspect(actor.Session)
	if err != nil {
		return "", err
	}
	return info.Address(), nil
}

// TriageIssue closes or exports an issue for the caller on ctx; starting a goal
// from one goes through IssueGoal and the goal creation path of dyna goal. by
// is the address that credits the change, and an empty one is derived from the
// caller, so an address never grants authority.
func (s *Service) TriageIssue(ctx context.Context, roomRef, id, action, value, by string) (room.Issue, error) {
	r, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return room.Issue{}, err
	}
	actor, err := s.issueCaller(ctx, r.ID)
	if err != nil {
		return room.Issue{}, err
	}
	if err := authority.Check("ChangeIssue", actor, nil); err != nil {
		return room.Issue{}, err
	}
	if by == "" {
		if by, err = s.issueAddress(actor, r); err != nil {
			return room.Issue{}, err
		}
	}
	ctx, cancel := context.WithTimeout(authority.WithActor(ctx, actor), 30*time.Second)
	defer cancel()
	// A close may also end an issue a goal links: root and the owner reach
	// this path, and a done goal can leave such an issue behind (D-249). An
	// export keeps refusing a goal issue, because that work belongs to GitHub.
	if action == "close" {
		return s.rooms().CloseIssue(ctx, r.ID, id, by, strings.TrimSpace(value), room.CloseOrigin{})
	}
	return s.rooms().ChangeIssue(ctx, r.ID, id, by, func(i *room.Issue) error {
		switch action {
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

// goalIssues lists the open issues that goal id still links.
func (s *Service) goalIssues(roomID, goal string) ([]room.Issue, error) {
	rows, err := s.rooms().Issues(roomID, true)
	if err != nil {
		return nil, fmt.Errorf("read the issues of goal %s: %w", goal, err)
	}
	var linked []room.Issue
	for _, issue := range rows {
		if issue.State == room.IssueStateGoal && issue.GoalID == goal {
			linked = append(linked, issue)
		}
	}
	return linked, nil
}

// closeNamedGoalIssues closes the issues that the finisher named with --close
// and returns the labels of the linked issues it left open. A merged PR of the
// goal closes the issues it fixes on its own, so a done goal never closes an
// issue by itself: its done note may say the issue is not fixed (D-285). The
// close follows the stored goal state, not the finisher's triage right, so the
// engine acts as the room's root seat; by credits the address that ended the goal.
func (s *Service) closeNamedGoalIssues(ctx context.Context, r room.Record, g goals.Goal, closes []string, note string, author goalAuthor) ([]string, error) {
	reason := strings.TrimSpace(note)
	if reason == "" {
		reason = "goal " + g.ID + " done"
	}
	linked, err := s.goalIssues(r.ID, g.ID)
	if err != nil {
		return nil, err
	}
	ctx = authority.WithActor(ctx, authority.EngineRoomActor(r.ID))
	by := author.Address
	if author.ID == "owner" {
		by = r.Name
	}
	named := map[string]bool{}
	for _, ref := range closes {
		named[strings.ToUpper(strings.TrimSpace(ref))] = true
	}
	var failures []error
	var left []string
	for _, issue := range linked {
		var err error
		if named[issue.Label()] {
			_, err = s.rooms().CloseIssueGoal(ctx, r.ID, issue.Label(), g.ID, by, reason, room.CloseOrigin{})
		} else {
			// The ended goal must not keep the issue in its goal state, or no
			// other goal could take it.
			_, err = s.rooms().ReleaseIssueGoal(ctx, r.ID, issue.Label(), g.ID, by)
			if err == nil {
				left = append(left, issue.Label())
			}
		}
		// Another goal may take the issue after the read above, and a merged
		// PR may close it first: both leave this goal nothing to close.
		if errors.Is(err, room.ErrIssueLeftGoal) || (err != nil && strings.Contains(err.Error(), "is already ")) {
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("close %s: %w", issue.Label(), err))
		}
	}
	return left, errors.Join(failures...)
}

// checkNamedGoalIssues refuses a --close reference that goal g does not link, so
// a typo cannot pass silently after the goal has ended.
func (s *Service) checkNamedGoalIssues(roomID string, g goals.Goal, closes []string) error {
	if len(closes) == 0 {
		return nil
	}
	linked, err := s.goalIssues(roomID, g.ID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, issue := range linked {
		have[issue.Label()] = true
	}
	for _, ref := range closes {
		if label := strings.ToUpper(strings.TrimSpace(ref)); !have[label] {
			return fmt.Errorf("--close %s: goal %s does not link that issue", ref, g.ID)
		}
	}
	return nil
}

// issueText is the issue's own body: the note it was filed with, without the
// event's one-line summary. A typed event carries the note even when the filer
// gave none, so the summary never reads as a note. A post stored before the
// typed events still carries its note in the post text.
func issueText(msg room.Message) string {
	if ev, ok := msg.Issue(); ok {
		return ev.Detail
	}
	return msg.Text
}

func issueDetail(i room.Issue, text string) string {
	detail := "Local issue " + i.Label()
	if text = strings.TrimSpace(text); text != "" {
		detail += "\n\n" + text
	}
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
