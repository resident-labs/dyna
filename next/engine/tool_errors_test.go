package engine

import (
	"errors"
	"net/http"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

func TestARoomRefusalNamesTheCallersRoom(t *testing.T) {
	s, room, root, ctx := setup(t)
	_, problem := s.SessionList(ctx, sessionCaller(root), contract.SessionListRequest{Room: "horizon"})
	if want := `room "horizon" is not your room; yours is ` + string(room); problem == nil || problem.Code != contract.ErrForbidden || problem.Message != want {
		t.Fatalf("%+v", problem)
	}
}

func TestGoalRefusalsNameTheFix(t *testing.T) {
	s, room, _, ctx := setup(t)
	const required, unknown = "goal is required; goal list shows the room's goals", "goal g9 not found; goal list shows the room's goals"
	for _, tc := range []struct {
		name    string
		call    func() *contract.Error
		code    contract.ErrorCode
		message string
	}{
		{"done", func() *contract.Error {
			_, e := s.GoalDone(ctx, ownerCaller, contract.GoalDoneRequest{Room: room, Summary: "done"})
			return e
		}, contract.ErrInvalid, required},
		{"item", func() *contract.Error {
			_, e := s.GoalItem(ctx, ownerCaller, contract.GoalItemRequest{Room: room, Text: "step"})
			return e
		}, contract.ErrInvalid, required},
		{"journal write", func() *contract.Error {
			_, e := s.GoalJournal(ctx, ownerCaller, contract.GoalJournalRequest{Room: room, Text: "note"})
			return e
		}, contract.ErrInvalid, required},
		{"journal read", func() *contract.Error {
			_, e := s.GoalJournal(ctx, ownerCaller, contract.GoalJournalRequest{Room: room})
			return e
		}, contract.ErrInvalid, required},
		{"unknown goal", func() *contract.Error {
			_, e := s.GoalDone(ctx, ownerCaller, contract.GoalDoneRequest{Room: room, Goal: "g9", Summary: "done"})
			return e
		}, contract.ErrNotFound, unknown},
		{"unknown goal read", func() *contract.Error {
			_, e := s.GoalJournal(ctx, ownerCaller, contract.GoalJournalRequest{Room: room, Goal: "g9"})
			return e
		}, contract.ErrNotFound, unknown},
		{"unknown holder", func() *contract.Error {
			_, e := s.GoalSet(ctx, ownerCaller, contract.GoalSetRequest{Room: room, Session: "ses_gone", Text: "work", DoneWhen: "done"})
			return e
		}, contract.ErrNotFound, "session ses_gone not found in this room; session list shows them"},
	} {
		if e := tc.call(); e == nil || e.Code != tc.code || e.Message != tc.message {
			t.Errorf("%s: %+v", tc.name, e)
		}
	}
}

func TestPrOpenNamesTheMissingBranch(t *testing.T) {
	s, session, ctx := prFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("a missing branch reached GitHub") })
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{err: errors.New("no such ref")})
	_, problem := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main"}}})
	want := "invalid operation: branch v2/" + string(session) + " not found on owner/repo; head names another branch"
	if problem == nil || problem.Code != contract.ErrInvalid || problem.Message != want {
		t.Fatalf("%+v", problem)
	}
}

func TestIssueTrackNamesARepositoryOutsideTheRoom(t *testing.T) {
	r := newPRRig(t)
	r.github.AddIssue(fakegithub.Issue{Repo: "other/repo", Number: 3, Title: "elsewhere", Author: "someone", State: "OPEN"})
	_, problem := r.s.IssueTrack(r.ctx, sessionCaller(r.child), contract.IssueTrackRequest{Repo: "other/repo", Number: 3})
	if want := "repository other/repo is not in this room; the owner can track it"; problem == nil || problem.Code != contract.ErrForbidden || problem.Message != want {
		t.Fatalf("%+v", problem)
	}
}
