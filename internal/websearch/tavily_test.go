package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTavilySendsTheQueryWithItsKey(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var got tavilyRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go","url":"https://go.dev","content":"The Go language."},
			{"title":"Docs","url":"https://go.dev/doc","content":"Documentation."}
		]}`))
	}))
	defer server.Close()

	tavily := Tavily{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "tavily-key", nil }}
	found, err := tavily.Search(t.Context(), Query{Text: "go language", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/search" || gotAuth != "Bearer tavily-key" {
		t.Fatalf("request = %s %s auth=%q", gotMethod, gotPath, gotAuth)
	}
	if got.Query != "go language" || got.MaxResults != 1 {
		t.Fatalf("body = %+v", got)
	}
	if len(found) != 1 {
		t.Fatalf("results = %+v, want one result", found)
	}
	if found[0].Title != "Go" || found[0].URL != "https://go.dev" || found[0].Snippet != "The Go language." || found[0].Provider != "tavily" {
		t.Fatalf("result = %+v", found[0])
	}
}

func TestTavilyWithoutAKeyIsNotSignedIn(t *testing.T) {
	tavily := Tavily{Key: func(context.Context) (string, error) { return "", ErrNotSignedIn }}
	if _, err := tavily.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Tavily{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a key reader: err = %v, want ErrNotSignedIn", err)
	}
}

// A failing API names its status and never echoes the response body.
// A failing API names its status and the reason, and never the key.
func TestTavilyReportsAFailureWithoutTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"bad key tavily-key"}`))
	}))
	defer server.Close()

	tavily := Tavily{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "tavily-key", nil }}
	_, err := tavily.Search(t.Context(), Query{Text: "go"})
	assertRefusal(t, "tavily", http.StatusUnauthorized, "tavily-key", err)
}
