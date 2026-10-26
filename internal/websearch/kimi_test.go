package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// kimiToken is one Kimi Code sign-in for a test.
func kimiToken(access string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return access, err }
}

func TestKimiSendsTheQueryWithItsToken(t *testing.T) {
	var gotPath, gotToken string
	var gotBody struct {
		TextQuery string `json:"text_query"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		for _, header := range []string{"User-Agent", "X-Msh-Platform", "X-Msh-Version", "X-Msh-Device-Name", "X-Msh-Device-Model", "X-Msh-Os-Version", "X-Msh-Device-Id"} {
			if r.Header.Get(header) == "" {
				t.Errorf("%s header is missing", header)
			}
		}
		if got := r.Header.Get("User-Agent"); !strings.HasPrefix(got, "kimi-code-cli/") {
			t.Errorf("User-Agent = %q, want a kimi-code-cli version", got)
		}
		if got := r.Header.Get("X-Msh-Platform"); got != "kimi_code_cli" {
			t.Errorf("X-Msh-Platform = %q, want kimi_code_cli", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"search_results":[
			{"title":"Go","url":"https://go.dev","snippet":"The Go language."},
			{"title":"Docs","url":"https://go.dev/doc","snippet":"Documentation."},
			{"title":"No URL","url":"","snippet":"Skipped."}
		]}`))
	}))
	defer server.Close()

	kimi := Kimi{BaseURL: server.URL + "/coding/v1/", Token: kimiToken("kimi-access", nil), DeviceID: "device-1"}
	got, err := kimi.Search(t.Context(), Query{Text: "go language", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/coding/v1/search" || gotToken != "Bearer kimi-access" || gotBody.TextQuery != "go language" {
		t.Fatalf("request = %s %q body %+v, want POST /coding/v1/search with the token and the query", gotPath, gotToken, gotBody)
	}
	if len(got) != 2 {
		t.Fatalf("results = %+v, want two results", got)
	}
	if got[0].Title != "Go" || got[0].URL != "https://go.dev" || got[0].Snippet != "The Go language." || got[0].Provider != "kimi" {
		t.Fatalf("first result = %+v", got[0])
	}
	if got[1].URL != "https://go.dev/doc" || got[1].Snippet != "Documentation." {
		t.Fatalf("second result = %+v", got[1])
	}
}

// An answer without search_results holds no result and is not a failure.
func TestKimiReadsAnEmptyAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	kimi := Kimi{BaseURL: server.URL, Token: kimiToken("kimi-access", nil)}
	got, err := kimi.Search(t.Context(), Query{Text: "go"})
	if err != nil || len(got) != 0 {
		t.Fatalf("results = %+v, %v; want no result and no error", got, err)
	}
}

func TestKimiWithoutASignInIsNotSignedIn(t *testing.T) {
	if _, err := (Kimi{}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("without a token reader: err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Kimi{Token: kimiToken("", nil)}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("with an empty token: err = %v, want ErrNotSignedIn", err)
	}
	if _, err := (Kimi{Token: kimiToken("", ErrNotSignedIn)}).Search(t.Context(), Query{Text: "go"}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("with a refused sign-in: err = %v, want ErrNotSignedIn", err)
	}
}

// A refused token is a skipped provider, and any other failing status names its
// code and never echoes the response body or the token.
func TestKimiReportsAFailureWithoutTheSecret(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired token kimi-access"}`))
	}))
	defer refused.Close()
	kimi := Kimi{BaseURL: refused.URL, Token: kimiToken("kimi-access", nil)}
	_, err := kimi.Search(t.Context(), Query{Text: "go"})
	if !errors.Is(err, ErrNotSignedIn) || strings.Contains(err.Error(), "kimi-access") {
		t.Fatalf("err = %v, want a not-signed-in failure without the token", err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"token=kimi-access"}`))
	}))
	defer failing.Close()
	kimi = Kimi{BaseURL: failing.URL, Token: kimiToken("kimi-access", nil)}
	_, err = kimi.Search(t.Context(), Query{Text: "go"})
	assertRefusal(t, "kimi", http.StatusInternalServerError, "kimi-access", err)
}
