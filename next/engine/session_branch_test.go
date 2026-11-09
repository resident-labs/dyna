package engine

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionSetBranchIsTheOwnersAndMovesTheSessionsBranch(t *testing.T) {
	s, session, ctx := prFixture(t, nil)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	// The repository's default is develop and GitHub has release: the owner may give neither, nor the protected names.
	s.SetRepositories(&fakeRepos{taken: map[string]bool{"main": true, "master": true, "HEAD": true, "develop": true, "release": true}})
	other, problem := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: record.Room, Parent: session, Name: "holder", Profile: "test", Goal: "goal"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, err := s.store.ClaimBranch(ctx, record.Repo, other.Session, "root-branch", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"main", "master", "HEAD", "develop", "release", "a..b", "x.lock", "v2/slash", "-dash", ".x", "_x", ""} {
		if _, problem := s.SessionSetBranch(ctx, owner, contract.SessionSetBranchRequest{Session: session, Branch: bad}); problem == nil || problem.Code != contract.ErrInvalid {
			t.Errorf("%q: %+v", bad, problem)
		}
	}
	if _, problem := s.SessionSetBranch(ctx, owner, contract.SessionSetBranchRequest{Session: session, Branch: "root-branch"}); problem == nil || problem.Code != contract.ErrInvalid || problem.Message != "another session has that branch" {
		t.Fatalf("another session's branch: %+v", problem)
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

func TestPrOpenDefaultsToTheNewestRecordedBranch(t *testing.T) {
	s, session, ctx := prFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("a missing branch reached GitHub") })
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddBranch(ctx, record.Repo, session, "newer", time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{err: errors.New("no such ref")})
	_, problem := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main"}}})
	if problem == nil || !strings.Contains(problem.Message, "branch newer not found") {
		t.Fatalf("%+v", problem)
	}
}
