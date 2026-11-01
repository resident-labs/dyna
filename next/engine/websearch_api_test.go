package engine

import (
	"github.com/resident-labs/dyna/contract"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebSearchUsesStoredKey(t *testing.T) {
	s, _, worker, ctx := setup(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "private-key" || r.URL.Query().Get("q") != "example" {
			t.Error("wrong request")
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Example","url":"https://example.org","description":"Result"}]}}`))
	}))
	defer server.Close()
	s.searchBraveURL = server.URL
	if err := s.store.SetSearchKey(ctx, "brave", "private-key"); err != nil {
		t.Fatal(err)
	}
	response, problem := s.WebSearch(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.WebSearchRequest{Query: "example"})
	if problem != nil || len(response.Results) != 1 || response.Results[0].URL != "https://example.org" {
		t.Fatalf("search: %+v %v", response, problem)
	}
}

func TestWebSearchDistinguishesFailureFromEmptyResults(t *testing.T) {
	s, _, worker, ctx := setup(t)
	failed := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failed {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()
	s.searchBraveURL = server.URL
	if err := s.store.SetSearchKey(ctx, "brave", "private-key"); err != nil {
		t.Fatal(err)
	}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	req := contract.WebSearchRequest{Query: "example"}
	result, problem := s.WebSearch(ctx, caller, req)
	if problem != nil || result.Provider != "" || len(result.Attempts) == 0 {
		t.Fatalf("failure: %+v %v", result, problem)
	}
	failed = false
	result, problem = s.WebSearch(ctx, caller, req)
	if problem != nil || result.Provider != "brave" || len(result.Results) != 0 {
		t.Fatalf("empty: %+v %v", result, problem)
	}
}

func TestSearchKeysAreOwnerOnly(t *testing.T) {
	s, _, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	req := contract.WebsearchSetKeyRequest{Provider: "muse", Key: "private-key"}
	if _, problem := s.WebsearchSetKey(ctx, session, req); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session set: %v", problem)
	}
	if _, problem := s.WebsearchSetKey(ctx, owner, req); problem != nil {
		t.Fatal(problem)
	}
	got, err := s.store.SearchKey(ctx, "muse")
	if err != nil || got != req.Key {
		t.Fatalf("saved key: %v", err)
	}
	if _, problem := s.WebsearchRemoveKey(ctx, session, contract.WebsearchRemoveKeyRequest{Provider: "muse"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("session remove: %v", problem)
	}
	if _, problem := s.WebsearchRemoveKey(ctx, owner, contract.WebsearchRemoveKeyRequest{Provider: "muse"}); problem != nil {
		t.Fatal(problem)
	}
	if _, err := s.store.SearchKey(ctx, "muse"); err == nil {
		t.Fatal("key remains")
	}
}
