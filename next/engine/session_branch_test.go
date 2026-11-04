package engine

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionSetBranchIsTheOwnersAndMovesTheSessionsBranch(t *testing.T) {
	s, session, ctx := prFixture(t, nil)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.SessionSetBranch(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.SessionSetBranchRequest{Session: session, Branch: "moved"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session moved its own branch: %+v", problem)
	}
	for _, bad := range []string{"main", "HEAD", "a..b", "x.lock", "v2/slash", "-dash", ""} {
		if _, problem := s.SessionSetBranch(ctx, owner, contract.SessionSetBranchRequest{Session: session, Branch: bad}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Errorf("%q: %+v", bad, problem)
		}
	}
	if _, problem := s.SessionSetBranch(ctx, owner, contract.SessionSetBranchRequest{Session: session, Branch: "portizon-wasm"}); problem != nil {
		t.Fatal(problem)
	}
	got, err := s.store.SessionBranches(ctx, record.Repo, session)
	if err != nil || len(got) != 2 || got[0] != "portizon-wasm" || got[1] != "pr-worker" {
		t.Fatalf("%v, %v", got, err)
	}
	// The old branch still pushes, as live work on it does.
	if _, problem := s.SessionSetBranch(ctx, owner, contract.SessionSetBranchRequest{Session: session, Branch: "portizon-wasm"}); problem != nil {
		t.Fatalf("repeat: %+v", problem)
	}
	listed, problem := s.EventsList(ctx, owner, contract.EventsListRequest{Room: record.Room, Types: []contract.EventType{"session.notice"}, Limit: 100})
	if problem != nil {
		t.Fatal(problem)
	}
	var told bool
	for _, event := range listed.Events {
		if body := event.Body.(contract.SessionNotice); body.Session == session && strings.Contains(body.Text, "Your branch is now portizon-wasm") {
			told = true
		}
	}
	if !told {
		t.Fatal("the session was not told")
	}
}

func TestPrOpenWithoutARecordedBranchSaysSo(t *testing.T) {
	s, session, ctx := prFixture(t, nil)
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM session_branches")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	_, problem := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main"}}})
	if problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "no branch recorded yet") {
		t.Fatalf("%+v", problem)
	}
}
