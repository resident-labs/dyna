package watch

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	roomfixture "dyna-agent/internal/room/testfixture"
	"dyna-agent/internal/subagent"
)

// ownerCtx acts for the owner, which issue links need now that they check the
// caller on ctx instead of a credited address.
func ownerCtx() context.Context {
	return authority.WithActor(context.Background(), authority.Actor{Role: authority.Owner})
}

// fixesEnv gives a service whose GitHub calls are recorded and answered from
// a small map, so the merge close needs no network and no waiting.
type fixesEnv struct {
	svc    *Service
	rooms  *room.Store
	room   room.Record
	calls  []string
	states map[string]string
	fail   map[string]error
}

func newFixesEnv(t *testing.T) *fixesEnv {
	t.Helper()
	e := newEnv(t)
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "fixed-by"})
	if err != nil {
		t.Fatal(err)
	}
	roomfixture.Repository(t, rooms, rec.ID, "github.com", "owner/repo", "read-write")
	e.s.Notify = notify.New(rooms, subagent.NewManager())
	env := &fixesEnv{svc: e.s, rooms: rooms, room: rec, states: map[string]string{"7": "OPEN", "8": "CLOSED"}, fail: map[string]error{}}
	env.svc.GH.run = func(ctx context.Context, cmd *exec.Cmd) error {
		args := cmd.Args[1:]
		joined := strings.Join(args, " ")
		env.calls = append(env.calls, joined)
		if err := env.fail[joined]; err != nil {
			return err
		}
		if len(args) > 1 && args[0] == "issue" && args[1] == "view" {
			_, _ = io.WriteString(cmd.Stdout, env.states[args[2]]+"\n")
		}
		return nil
	}
	return env
}

func (e *fixesEnv) mergedRecord(fixes ...state.IssueRef) Record {
	return Record{
		Key: "github.com/owner/repo#42", ChangeID: "github.com/owner/repo#42",
		Kind: "github", Host: "github.com", Repo: "owner/repo", Number: 42,
		RoomID: e.room.ID, State: "merged", Fixes: fixes,
	}
}

func TestMergedPRClosesItsLinkedIssues(t *testing.T) {
	e := newFixesEnv(t)
	issue, err := e.rooms.FileIssue(e.room.ID, room.IssueInput{Title: "Parser drops the last line", File: "internal/parser.go", Line: 12, Filer: e.room.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.rooms.LinkIssueGoal(ownerCtx(), e.room.ID, issue.ID, "g7", e.room.Name, "open"); err != nil {
		t.Fatal(err)
	}
	r := e.mergedRecord(
		state.IssueRef{Kind: state.IssueKindDyna, Number: issue.Number},
		state.IssueRef{Kind: state.IssueKindGitHub, Number: 7},
	)
	if err := e.svc.closeFixedIssues(context.Background(), r, "abcdef1234567890"); err != nil {
		t.Fatalf("closeFixedIssues: %v", err)
	}

	all, err := e.rooms.Issues(e.room.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].State != "closed" {
		t.Fatalf("room issue not closed: %+v", all)
	}
	if want := "Fixed by #42"; all[0].Reason != want {
		t.Errorf("close reason = %q, want %q", all[0].Reason, want)
	}
	// The room post names the engine that closed the issue, and the reason once.
	msgs, err := e.rooms.Messages(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	closedText := ""
	for _, msg := range msgs {
		if ev, ok := msg.Issue(); ok && ev.State == room.IssueStateClosed {
			closedText = msg.Text
		}
	}
	if want := fmt.Sprintf("The engine closed issue D-%d: Fixed by #42", issue.Number); closedText != want {
		t.Errorf("posted close = %q, want %q", closedText, want)
	}
	want := []string{
		"issue view 7 --repo owner/repo --json state --jq .state",
		"issue close 7 --repo owner/repo --reason completed --comment Fixed by #42 in abcdef1.",
	}
	if fmt.Sprint(e.calls) != fmt.Sprint(want) {
		t.Errorf("GitHub calls = %q, want %q", e.calls, want)
	}
}

func TestClosedPRClosesNoIssues(t *testing.T) {
	e := newFixesEnv(t)
	issue, err := e.rooms.FileIssue(e.room.ID, room.IssueInput{Title: "Parser drops the last line", Filer: e.room.Name})
	if err != nil {
		t.Fatal(err)
	}
	r := e.mergedRecord(state.IssueRef{Kind: state.IssueKindDyna, Number: issue.Number})
	r.State = "closed"
	if err := e.svc.closeFixedIssues(context.Background(), r, "abcdef1234567890"); err != nil {
		t.Fatalf("closeFixedIssues: %v", err)
	}
	if len(e.calls) != 0 {
		t.Errorf("a PR that closed without merging made GitHub calls: %q", e.calls)
	}
	all, err := e.rooms.Issues(e.room.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].State != "open" {
		t.Fatalf("a PR that closed without merging closed its issue: %+v", all)
	}
}

// GitHub closes a "Fixes #N" issue natively, so a closed issue takes no comment.
func TestMergedPRLeavesAnAlreadyClosedGitHubIssueAlone(t *testing.T) {
	e := newFixesEnv(t)
	r := e.mergedRecord(state.IssueRef{Kind: state.IssueKindGitHub, Number: 8})
	if err := e.svc.closeFixedIssues(context.Background(), r, "abcdef1234567890"); err != nil {
		t.Fatalf("closeFixedIssues: %v", err)
	}
	for _, call := range e.calls {
		if strings.HasPrefix(call, "issue close") {
			t.Fatalf("a closed issue was closed again: %q", call)
		}
	}
}

// A tracked PR takes its links from the body as well as from the flag.
func TestTrackLinksIssuesFromTheBody(t *testing.T) {
	e := newEnv(t)
	e.f.Pull.Body = "The parser drops the last line.\n\nFixes D-3, #207"
	e.write()
	r := e.track()
	if got, want := state.FixesLabel(r.Fixes), "D-3, #207"; got != want {
		t.Fatalf("tracked fixes = %q, want %q", got, want)
	}
}

// The automatic merge path closes the linked room issue, not only the helper.
func TestAutomaticMergeClosesTheLinkedRoomIssue(t *testing.T) {
	e, _, merged := mergeEnv(t, 1)
	r, err := e.s.Notify.Rooms.Resolve(e.author.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	issue, err := e.s.Notify.Rooms.FileIssue(r.ID, room.IssueInput{Title: "Parser drops the last line", Filer: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.Store.update(func(st *State) error {
		st.Records["github.com/owner/repo#1"].Fixes = []state.IssueRef{{Kind: state.IssueKindDyna, Number: issue.Number}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.MergeFix(t.Context(), "github.com/owner/repo#1"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(*merged) != 1 {
		t.Fatalf("merged PRs = %v", *merged)
	}
	all, err := e.s.Notify.Rooms.Issues(r.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].State != "closed" || all[0].Reason != "Fixed by #1" {
		t.Fatalf("merged PR did not close its issue: %+v", all)
	}
}
