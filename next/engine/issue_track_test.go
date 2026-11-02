package engine

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

// track adds an issue on GitHub and tracks it for the child, past its first read.
func (r *prRig) track(t *testing.T, issue fakegithub.Issue) {
	t.Helper()
	r.github.AddIssue(issue)
	if _, e := r.s.IssueTrack(r.ctx, sessionCaller(r.child), contract.IssueTrackRequest{Repo: issue.Repo, Number: issue.Number}); e != nil {
		t.Fatalf("%+v", e)
	}
	r.settle(t)
}

// issueWakes returns the waking deliveries to a session that carry issue changes, as one list of changes per delivery.
func (r *prRig) issueWakes(t *testing.T, to contract.SessionID) [][]contract.IssueChange {
	t.Helper()
	rows, err := r.s.store.Read().QueryContext(r.ctx, "SELECT e.type,e.body FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type IN ('github.issue.changed','github.pr.changed') AND d.to_id=? AND d.wake=1 ORDER BY e.seq", to)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][]contract.IssueChange
	for rows.Next() {
		var kind string
		var data []byte
		if err := rows.Scan(&kind, &data); err != nil {
			t.Fatal(err)
		}
		var body struct {
			Changes json.RawMessage        `json:"changes"`
			Issues  []contract.IssueChange `json:"issues"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if kind == "github.pr.changed" {
			out = append(out, body.Issues)
			continue
		}
		var changes []contract.IssueChange
		if err := json.Unmarshal(body.Changes, &changes); err != nil {
			t.Fatal(err)
		}
		out = append(out, changes)
	}
	return out
}

func TestEveryIssueChangeIsToldOnceAndARestartRepeatsNothing(t *testing.T) {
	for name, test := range map[string]struct {
		edit   func(*fakegithub.Issue)
		want   contract.PrChangeKind
		action contract.GitHubAction
		detail string
	}{
		"comment":  {func(i *fakegithub.Issue) { i.Comments = []fakegithub.Comment{{ID: 1, Author: "x", Body: "hello"}} }, contract.PrChangeComment, "", ""},
		"label":    {func(i *fakegithub.Issue) { i.Meta.Labels = []string{"bug"} }, contract.PrChangeMetadata, contract.GitHubLabeled, "bug"},
		"assignee": {func(i *fakegithub.Issue) { i.Meta.Assignees = []string{"x"} }, contract.PrChangeMetadata, contract.GitHubAssigned, "x"},
		"title":    {func(i *fakegithub.Issue) { i.Title = "new name" }, contract.PrChangeMetadata, contract.GitHubRenamed, "new name"},
		"body":     {func(i *fakegithub.Issue) { i.Meta.Body = "edited" }, contract.PrChangeMetadata, contract.GitHubEdited, ""},
		"close":    {func(i *fakegithub.Issue) { i.State = "CLOSED" }, contract.PrChangeClosed, contract.GitHubClosed, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPRRig(t)
			r.track(t, fakegithub.Issue{Repo: "owner/repo", Number: 12})
			if got := r.issueWakes(t, r.child); len(got) != 0 {
				t.Fatalf("woke before any change: %+v", got)
			}
			r.github.EditIssue("owner/repo", 12, test.edit)
			r.pass(t)
			got := r.issueWakes(t, r.child)
			if len(got) != 1 || len(got[0]) != 1 || !slices.Equal(got[0][0].Kinds, []contract.PrChangeKind{test.want}) {
				t.Fatalf("wakes %+v", got)
			}
			if test.action != "" && (len(got[0][0].Actions) != 1 || got[0][0].Actions[0].Action != test.action || got[0][0].Actions[0].Detail != test.detail) {
				t.Fatalf("actions %+v", got[0][0].Actions)
			}
			r.clock.add(11 * time.Minute)
			r.pass(t)
			r.connect()
			r.clock.add(11 * time.Minute)
			r.pass(t)
			if got := r.issueWakes(t, r.child); len(got) != 1 {
				t.Fatalf("told twice: %+v", got)
			}
		})
	}
}

func TestIssueAuthority(t *testing.T) {
	r := newPRRig(t)
	other, e := r.s.RoomCreate(r.ctx, prOwner, contract.RoomCreateRequest{Name: "other-room", RootProfile: "test"})
	if e != nil {
		t.Fatalf("%+v", e)
	}
	sibling, e := r.s.SessionStart(r.ctx, prOwner, contract.SessionStartRequest{Room: r.room, Parent: r.root, Name: "issue-sibling", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatalf("%+v", e)
	}
	child := sessionCaller(r.child)
	r.github.AddIssue(fakegithub.Issue{Repo: "stranger/repo", Number: 1})
	r.github.AddIssue(fakegithub.Issue{Repo: "stranger/repo", Number: 4})
	r.github.AddIssue(fakegithub.Issue{Repo: "owner/repo", Number: 2})
	r.github.Add(fakegithub.Pull{Repo: "owner/repo", Number: 3})

	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Repo: "stranger/repo", Number: 1}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("outside repository: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Number: 2, Session: sibling.Session}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("for another session: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Number: 2, Room: other.Room}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("for another room: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Repo: "owner/repo", Number: 404}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("missing issue: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Number: 3}); e == nil || e.Code != contract.ErrNotFound || !strings.Contains(e.Message, "pr track") {
		t.Fatalf("a PR's number: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, prOwner, contract.IssueTrackRequest{Repo: "owner/repo", Number: 2}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("owner without a room: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, prOwner, contract.IssueTrackRequest{Repo: "stranger/repo", Number: 1, Room: r.room}); e != nil {
		t.Fatalf("owner: %+v", e)
	}
	if status, e := r.s.IssueStatus(r.ctx, prOwner, contract.IssueStatusRequest{Repo: "stranger/repo", Number: 1}); e != nil || status.Issue.Session != r.root {
		t.Fatalf("owner tracks for the room's root: %+v %+v", status, e)
	}
	if tracked, e := r.s.IssueTrack(r.ctx, prOwner, contract.IssueTrackRequest{Repo: "stranger/repo", Number: 4, Session: r.child}); e != nil || tracked.Issue.Session != r.child {
		t.Fatalf("owner tracks for a named session: %+v %+v", tracked, e)
	}
	if _, e := r.s.IssueTrack(r.ctx, child, contract.IssueTrackRequest{Number: 2}); e != nil {
		t.Fatalf("own repository: %+v", e)
	}
	if _, e := r.s.IssueTrack(r.ctx, sessionCaller(sibling.Session), contract.IssueTrackRequest{Repo: "owner/repo", Number: 2}); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("another session's issue: %+v", e)
	}

	stranger, e := r.s.SessionList(r.ctx, prOwner, contract.SessionListRequest{Room: other.Room})
	if e != nil || len(stranger.Sessions) == 0 {
		t.Fatalf("%+v %+v", stranger, e)
	}
	outsider := sessionCaller(stranger.Sessions[0].ID)
	if _, e := r.s.IssueStatus(r.ctx, outsider, contract.IssueStatusRequest{Repo: "owner/repo", Number: 2}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("status from another room: %+v", e)
	}
	if _, e := r.s.IssueList(r.ctx, outsider, contract.IssueListRequest{Room: r.room}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("list of another room: %+v", e)
	}
	if _, e := r.s.IssueUntrack(r.ctx, sessionCaller(sibling.Session), contract.IssueUntrackRequest{Repo: "owner/repo", Number: 2}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("sibling untrack: %+v", e)
	}
	if _, e := r.s.IssueUntrack(r.ctx, sessionCaller(r.root), contract.IssueUntrackRequest{Repo: "owner/repo", Number: 2}); e != nil {
		t.Fatalf("ancestor untrack: %+v", e)
	}
	if _, e := r.s.IssueStatus(r.ctx, child, contract.IssueStatusRequest{Number: 2}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("after untrack: %+v", e)
	}
}
