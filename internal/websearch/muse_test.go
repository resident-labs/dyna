package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// museID is the shape of the request id Muse expects in both places.
var museID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestMuseSendsTheQueryWithItsKey(t *testing.T) {
	var sawTrail bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/muse-code/search" {
			t.Errorf("request = %s %s, want POST /muse-code/search", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer muse-key" || r.Header.Get("X-Client-Id") != "tbh:exec" || r.Header.Get("X-Api-Version") != "1.0.0" {
			t.Errorf("headers = %v", r.Header)
		}
		var body museRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Query != "go language" || body.IterationIndex != 0 {
			t.Errorf("body = %+v, want the query and iteration zero", body)
		}
		if !museID.MatchString(body.RequestID) || body.RequestID != r.Header.Get("X-Tbh-Session-Id") {
			t.Errorf("request id %q and session id %q, want one UUID in both", body.RequestID, r.Header.Get("X-Tbh-Session-Id"))
		}
		sawTrail = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"` + body.RequestID + `","augmented_context":"【0†Go†go.dev】","results":[
			{"url":"https://go.dev","title":"Go","snippet":"The Go language.","page_last_modified":"-1"},
			{"url":"https://go.dev/doc","title":"Docs","snippet":"Documentation.","page_last_modified":"-1"}
		]}`))
	}))
	defer server.Close()

	muse := Muse{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "muse-key", nil }}
	got, err := muse.Search(t.Context(), Query{Text: "go language", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !sawTrail {
		t.Fatal("the search never reached the server")
	}
	if len(got) != 1 {
		t.Fatalf("results = %+v, want the limit of one", got)
	}
	if got[0].Title != "Go" || got[0].URL != "https://go.dev" || got[0].Snippet != "The Go language." || got[0].Provider != "muse" {
		t.Fatalf("result = %+v", got[0])
	}
}

func TestMuseWithoutAKeyIsNotSignedIn(t *testing.T) {
	muse := Muse{Key: func(context.Context) (string, error) { return "", ErrNotSignedIn }}
	if _, err := muse.Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Muse{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a key reader: err = %v, want ErrNotSignedIn", err)
	}
}

// A failing API names its status and never echoes the body, which can hold the
// key.
func TestMuseReportsAFailureWithoutTheKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key muse-key"}`))
	}))
	defer server.Close()

	muse := Muse{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "muse-key", nil }}
	_, err := muse.Search(t.Context(), Query{Text: "go"})
	if err == nil || err.Error() != "muse answered HTTP 401" {
		t.Fatalf("err = %v, want the status only", err)
	}
}

// A hit without a URL cannot be shown, and a body of another shape is a
// failure rather than an empty answer.
func TestMuseKeepsOnlyHitsWithAURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"No address","snippet":"x"},{"url":"https://go.dev","title":"Go","snippet":"The Go language."}]}`))
	}))
	defer server.Close()

	muse := Muse{BaseURL: server.URL, Key: func(context.Context) (string, error) { return "muse-key", nil }}
	got, err := muse.Search(t.Context(), Query{Text: "go"})
	if err != nil || len(got) != 1 || got[0].URL != "https://go.dev" {
		t.Fatalf("results = %+v, %v; want only the hit with a URL", got, err)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":"none"}`))
	}))
	defer broken.Close()
	muse.BaseURL = broken.URL
	_, err = muse.Search(t.Context(), Query{Text: "go"})
	if err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("err = %v, want a response Dyna cannot read", err)
	}
}
