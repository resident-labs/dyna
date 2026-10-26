package websearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBraveSendsTheQueryWithItsKey(t *testing.T) {
	var gotQuery, gotCount, gotKey, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotCount = r.URL.Path, r.URL.Query().Get("q"), r.URL.Query().Get("count")
		gotKey = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[
			{"title":"Go","url":"https://go.dev","description":"The Go language."},
			{"title":"Docs","url":"https://go.dev/doc","description":"Documentation."}
		]}}`))
	}))
	defer server.Close()

	brave := Brave{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "brave-key", nil }}
	got, err := brave.Search(t.Context(), Query{Text: "go language", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/res/v1/web/search" || gotQuery != "go language" || gotCount != "1" || gotKey != "brave-key" {
		t.Fatalf("request = %s q=%q count=%q key=%q", gotPath, gotQuery, gotCount, gotKey)
	}
	if len(got) != 1 {
		t.Fatalf("results = %+v, want one result", got)
	}
	if got[0].Title != "Go" || got[0].URL != "https://go.dev" || got[0].Snippet != "The Go language." || got[0].Provider != "brave" {
		t.Fatalf("result = %+v", got[0])
	}
}

func TestBraveWithoutAKeyIsNotSignedIn(t *testing.T) {
	brave := Brave{Key: func(context.Context) (string, error) { return "", ErrNotSignedIn }}
	if _, err := brave.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Brave{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a key reader: err = %v, want ErrNotSignedIn", err)
	}
}

// A failing API names its status and the reason, and never the key.
func TestBraveReportsAFailureWithoutTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key brave-key"}`))
	}))
	defer server.Close()

	brave := Brave{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "brave-key", nil }}
	_, err := brave.Search(t.Context(), Query{Text: "go"})
	assertRefusal(t, "brave", http.StatusUnauthorized, "brave-key", err)
}
