package engine

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
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
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
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
	recent := false
	for _, attempt := range result.Attempts {
		if attempt.Provider == "brave" && attempt.Reason == contract.WebSearchRecentFailure {
			recent = true
		}
	}
	if !recent {
		t.Fatal("failure was not remembered")
	}
	now = now.Add(6 * time.Minute)
	result, problem = s.WebSearch(ctx, caller, req)
	if problem != nil {
		t.Fatal(problem)
	}
	for _, attempt := range result.Attempts {
		if attempt.Provider == "brave" && attempt.Reason == contract.WebSearchRecentFailure {
			t.Fatal("failure did not expire")
		}
	}
}

func TestWebSearchFallsBackFromChatGPTWithoutCitations(t *testing.T) {
	s, _, worker, ctx := setup(t)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	// Unix socket paths have a short system limit, even when TMPDIR is long.
	dir, err := os.MkdirTemp("/tmp", "dyna-ws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	proxy, err := model.NewProxy(dir, dir, "management")
	if err != nil {
		t.Fatal(err)
	}
	var stream atomic.Value
	stream.Store("")
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, stream.Load().(string))
	})}
	go func() { _ = upstream.Serve(proxy.Listener) }()
	t.Cleanup(func() { _ = upstream.Close() })
	s.SetWebSearchProxy(proxy, &model.Quotas{})
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) VALUES('account','proxy','openai','test')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"web":{"results":[{"title":"Fallback","url":"https://example.org"}]}}`)
	}))
	defer brave.Close()
	s.searchBraveURL = brave.URL
	if err := s.store.SetSearchKey(ctx, "brave", "private-key"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`data: {"type":"response.output_text.delta","delta":"unfinished"}` + "\n\n",
		`data: {"type":"response.completed","response":{"output":[]}}` + "\n\n",
	} {
		stream.Store(body)
		response, problem := s.WebSearch(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.WebSearchRequest{Query: "example"})
		if problem != nil || response.Provider != contract.WebSearchBrave || len(response.Results) != 1 {
			t.Fatalf("fallback: %+v %v", response, problem)
		}
		found := false
		for _, attempt := range response.Attempts {
			if attempt.Provider == contract.WebSearchChatGPT && attempt.Reason == contract.WebSearchFailed {
				found = true
			}
		}
		if !found {
			t.Fatal("ChatGPT failure was not reported")
		}
		now = now.Add(6 * time.Minute)
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
