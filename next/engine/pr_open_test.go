package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestPrOpenRecordsOwnerAndNotifiesParent(t *testing.T) {
	calls := 0
	var request struct {
		Head  string `json:"head"`
		Base  string `json:"base"`
		Title string `json:"title"`
		Draft bool   `json:"draft"`
	}
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/repos/owner/repo/pulls" || r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":42}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	result, failure := s.OpSubmit(ctx, caller, contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main", Draft: true}}})
	if failure != nil || result.Operation.Result == nil || result.Operation.State != contract.OpDone {
		t.Fatalf("result %+v error %+v", result, failure)
	}
	pr := result.Operation.Result.PR
	if calls != 1 || s.repos.(*fakeRepos).calls != 2 || request.Head != "v2/"+string(session) || request.Base != "main" || request.Title != "Trial" || !request.Draft || pr == nil || pr.Number != 42 || pr.URL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("calls %d request %+v pr %+v", calls, request, pr)
	}
	var owner string
	if err := s.store.Read().QueryRow("SELECT session FROM pr_owners WHERE repo=? AND number=?", "owner/repo", 42).Scan(&owner); err != nil || owner != string(session) {
		t.Fatalf("owner %q error %v", owner, err)
	}
	var count int
	if err := s.store.Read().QueryRow("SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='pr.opened' AND d.wake=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("wakes %d error %v", count, err)
	}
}

func TestPrOpenStoreFailureRequiresReconciliation(t *testing.T) {
	calls := 0
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":42}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	submit := func(title string) contract.OpSubmitResponse {
		out, failure := s.OpSubmit(ctx, caller, contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: title, Base: "main"}}})
		if failure != nil {
			t.Fatal(failure)
		}
		return out
	}
	submit("First")
	second := submit("Second")
	if calls != 2 || second.Operation.State != contract.OpFailed || second.Operation.Result == nil || !strings.Contains(second.Operation.Result.Error, "ownership could not be recorded") || !strings.Contains(second.Operation.Result.Error, "/pull/42") {
		t.Fatalf("calls %d result %+v", calls, second.Operation)
	}
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

func TestPrOpenTransfersAfterSessionClose(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"number":42}`)
	})
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	var parent string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", session).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	done := make(chan contract.OpSubmitResponse, 1)
	go func() {
		result, _ := s.OpSubmit(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: session}, contract.OpSubmitRequest{Operation: contract.Operation{Room: record.Room, Kind: "pr.open", Args: contract.PrOpenArgs{Title: "Trial", Base: "main"}}})
		done <- result
	}()
	<-started
	if _, e := s.SessionClose(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionCloseRequest{Session: session, Discard: true}); e != nil {
		close(release)
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if _, err := s.store.Session(ctx, session); !errors.Is(err, sql.ErrNoRows) {
		close(release)
		t.Fatalf("session remains open: %v", err)
	}
	close(release)
	result := <-done
	if result.Operation.State != contract.OpDone {
		t.Fatalf("operation %+v", result.Operation)
	}
	var owner string
	if err := s.store.Read().QueryRowContext(ctx, "SELECT session FROM pr_owners WHERE repo=? AND number=?", "owner/repo", 42).Scan(&owner); err != nil || owner != parent {
		t.Fatalf("owner %q error %v", owner, err)
	}
	var notifications int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries d JOIN events e ON e.seq=d.seq WHERE e.type='pr.opened' AND d.to_id=? AND d.wake=1", parent).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notifications %d error %v", notifications, err)
	}
}
