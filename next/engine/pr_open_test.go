package engine

import (
	"context"
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
