package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExaSendsTheQueryWithItsKey(t *testing.T) {
	var gotMethod, gotPath, gotKey string
	var got exaRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotKey = r.Method, r.URL.Path, r.Header.Get("x-api-key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go","url":"https://go.dev","highlights":["The Go language.","Fast builds."]},
			{"title":"Docs","url":"https://go.dev/doc","highlights":["Documentation."]}
		]}`))
	}))
	defer server.Close()

	exa := Exa{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "exa-key", nil }}
	found, err := exa.Search(t.Context(), Query{Text: "go language", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/search" || gotKey != "exa-key" {
		t.Fatalf("request = %s %s key=%q", gotMethod, gotPath, gotKey)
	}
	if got.Query != "go language" || got.NumResults != 1 || !got.Contents.Highlights {
		t.Fatalf("body = %+v", got)
	}
	if len(found) != 1 {
		t.Fatalf("results = %+v, want one result", found)
	}
	if found[0].Title != "Go" || found[0].URL != "https://go.dev" || found[0].Snippet != "The Go language. Fast builds." || found[0].Provider != "exa" {
		t.Fatalf("result = %+v", found[0])
	}
}

func TestExaWithoutAKeyIsNotSignedIn(t *testing.T) {
	exa := Exa{Key: func(context.Context) (string, error) { return "", ErrNotSignedIn }}
	if _, err := exa.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Exa{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a key reader: err = %v, want ErrNotSignedIn", err)
	}
}

// A failing API names its status and never echoes the response body.
func TestExaReportsAFailureWithoutTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key exa-key"}`))
	}))
	defer server.Close()

	exa := Exa{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "exa-key", nil }}
	_, err := exa.Search(t.Context(), Query{Text: "go"})
	if err == nil || err.Error() != "exa answered HTTP 401" {
		t.Fatalf("err = %v, want the status only", err)
	}
}
