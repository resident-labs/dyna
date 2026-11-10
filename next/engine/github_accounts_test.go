package engine

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/prs"
	"github.com/resident-labs/dyna/engine/store"
)

func TestAccountHelloAndContext(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetGitIdentity("Default", "default@example.com")
	a := store.GitHubIdentity{Login: "work-login", Name: "Work Name", Email: "work@example.com", Credential: "key"}
	if err := s.store.SetGitHubAccount(ctx, room, a); err != nil {
		t.Fatal(err)
	}
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if hello.GitName != a.Name || hello.GitEmail != a.Email {
		t.Fatalf("identity: %q %q", hello.GitName, hello.GitEmail)
	}
	snapshot, problem := s.ContextSnapshot(ctx, boxCaller(root), contract.ContextSnapshotRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, section := range snapshot.Sections {
		if section.Key == contract.ContextRoom && strings.Contains(section.Text, "GitHub account: work-login") {
			return
		}
	}
	t.Fatal("missing live account context")
}

func TestAccountPROpenNeverUsesEngineCredential(t *testing.T) {
	calls := 0
	s, session, ctx := prFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer work-secret" {
			t.Error("wrong account")
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})
	selected, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	files := &githubaccount.Files{Dir: t.TempDir()}
	if err := files.Write("credential", "work-secret"); err != nil {
		t.Fatal(err)
	}
	// The fixture attached its repository under the default account. Detach it
	// before selecting the work account, then attach it under that account.
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM room_repos WHERE room=?`, selected.Room)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetGitHubAccount(ctx, selected.Room, store.GitHubIdentity{Login: "work", Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddRoomRepo(ctx, selected.Room, "owner/repo", true); err != nil {
		t.Fatal(err)
	}
	resolver := &githubaccount.Resolver{Store: s.store, Tokens: files, DefaultToken: "engine-secret"}
	clients := &prs.Accounts{Resolve: resolver.Repo, Endpoint: s.pulls.Endpoint, Transport: http.DefaultTransport, Now: time.Now}
	s.SetGitHubAccounts(resolver, clients)
	entry := s.PrOpen("engine-secret", s.pulls.Endpoint, http.DefaultTransport)
	args := contract.PrOpenArgs{Title: "Title", Base: "main", Head: "pr-worker"}
	result := entry.Execute(ctx, "", args, boxCaller(session))
	if result.Error == "" || calls != 1 {
		t.Fatalf("rejected credential result: %+v, requests %d", result, calls)
	}
	if err := files.Clear("credential"); err != nil {
		t.Fatal(err)
	}
	result = entry.Execute(ctx, "", args, boxCaller(session))
	if result.Error == "" || calls != 1 || !strings.Contains(result.Error, "GitHub account work") {
		t.Fatalf("missing credential: %+v requests %d", result, calls)
	}
}
