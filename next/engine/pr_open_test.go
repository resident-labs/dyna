package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

func prFixture(t *testing.T, handler http.HandlerFunc) (*Service, contract.SessionID, context.Context) {
	t.Helper()
	s, room, root, ctx := setup(t)
	if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: root, Name: "pr-worker", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	// The engine gives a session its branch at its first box setup; the fixture's session has had that.
	if _, err := s.store.ClaimBranch(ctx, "owner/repo", child.Session, "pr-worker", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRepositories(&fakeRepos{})
	if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pr.open": s.PrOpen("secret-token", endpoint, server.Client().Transport)}, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	return s, child.Session, ctx
}

func TestPrOpenGitHubErrorIsRedacted(t *testing.T) {
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"secret-token is invalid","errors":[{"message":"No commits between main and head"}]}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	result, failure := s.OpSubmit(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main"}}})
	if failure != nil || result.Operation.Result == nil {
		t.Fatalf("response %+v error %+v", result, failure)
	}
	message := result.Operation.Result.Error
	if result.Operation.State != contract.OpFailed || result.Operation.Result.Cause != contract.CauseGithubFailed || strings.Contains(message, "secret-token") || !strings.Contains(message, "***") || !strings.Contains(message, "No commits between") {
		t.Fatalf("state %s error %q", result.Operation.State, message)
	}
}

func TestPrOpenRejectsUnownedHead(t *testing.T) {
	s, session, ctx := prFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unowned head reached GitHub") })
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	_, failure := s.OpSubmit(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main", Head: "v2/another"}}})
	if failure == nil {
		t.Fatal("unowned head was accepted")
	}
}

func TestPrOpenReportsTheDefaultHeadWhenGitHubRefuses(t *testing.T) {
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Validation Failed","errors":[{"message":"No commits between main and head"}]}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	open := func(head string) string {
		result, failure := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main", Head: head}}})
		if failure != nil {
			t.Fatal(failure)
		}
		return result.Operation.Result.Error
	}
	branch := "pr-worker"
	if _, err := s.store.ClaimBranch(ctx, record.Repo, session, branch, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if message := open(""); !strings.Contains(message, "head was not given and defaulted to "+branch+"; head names another branch") {
		t.Fatalf("default head: %q", message)
	}
	if message := open(branch); strings.Contains(message, "defaulted") {
		t.Fatalf("explicit head: %q", message)
	}
}

func TestPrOpenResultLeavesOutTheBodyAndSurvivesALateFailure(t *testing.T) {
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":433}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	open := func() contract.Operation {
		result, failure := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Body: strings.Repeat("long body ", 100), Base: "main"}}})
		if failure != nil || result.Operation.Result == nil {
			t.Fatalf("response %+v error %+v", result, failure)
		}
		return result.Operation
	}
	first := open()
	if first.State != contract.OpDone || first.Result.PR == nil || first.Result.PR.Number != 433 || first.Args.(contract.PrOpenArgs).Body != "" {
		t.Fatalf("first %+v", first)
	}
	// GitHub answers 433 again, as if another path already recorded it: recording fails after the PR exists.
	second := open()
	url := "https://github.com/owner/repo/pull/433"
	if second.State != contract.OpDone || second.Result.Error != "" || second.Result.PR == nil || second.Result.PR.URL != url ||
		!strings.Contains(second.Result.Output, url) || !strings.Contains(second.Result.Output, "recording its owner failed") {
		t.Fatalf("second %+v", second.Result)
	}
}

func TestPrOpenSendsEveryFieldToGitHubAndKeepsTheBodyInTheRecord(t *testing.T) {
	var sent map[string]any
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":434}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	branch := "pr-worker"
	if _, err := s.store.ClaimBranch(ctx, record.Repo, session, branch, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	result, failure := s.OpSubmit(ctx, sessionCaller(session), contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Body: "## What\nthe body", Base: "main", Draft: true}}})
	if failure != nil || result.Operation.State != contract.OpDone {
		t.Fatalf("response %+v error %+v", result, failure)
	}
	want := map[string]any{"title": "Trial", "body": "## What\nthe body", "base": "main", "head": branch, "draft": true}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("GitHub request %v, want %v", sent, want)
	}
	echo, err := json.Marshal(result.Operation.Args)
	if err != nil || strings.Contains(string(echo), `"body"`) {
		t.Fatalf("returned args %s (%v) show a body field that reads as dropped", echo, err)
	}
	stored, err := s.store.Operation(ctx, result.Operation.ID)
	if err != nil || !strings.Contains(string(stored.Args), "the body") {
		t.Fatalf("stored args %s (%v) lost the body", stored.Args, err)
	}
}
