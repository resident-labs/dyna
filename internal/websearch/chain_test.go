package websearch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProvider answers as its errors say: a not-signed-in provider is skipped.
type fakeProvider struct {
	name    string
	results []Result
	err     error
	calls   *int
}

func (f fakeProvider) Name() string { return f.name }

func (f fakeProvider) Search(context.Context, Query) ([]Result, error) {
	*f.calls++
	return f.results, f.err
}

func TestChainSkipsAProviderWithoutASignIn(t *testing.T) {
	asked := 0
	chain := Chain{
		fakeProvider{name: "chatgpt", err: ErrNotSignedIn, calls: &asked},
		fakeProvider{name: "brave", results: []Result{{Title: "Go", URL: "https://go.dev"}}, calls: &asked},
	}
	got, err := chain.Search(t.Context(), Query{Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://go.dev" || asked != 2 {
		t.Fatalf("results = %+v after %d providers, want brave's one result after both", got, asked)
	}
}

func TestChainStopsAtTheFirstAnswer(t *testing.T) {
	first, second := 0, 0
	chain := Chain{
		fakeProvider{name: "chatgpt", results: []Result{{Title: "One", URL: "https://one.test"}}, calls: &first},
		fakeProvider{name: "brave", results: []Result{{Title: "Two", URL: "https://two.test"}}, calls: &second},
	}
	got, err := chain.Search(t.Context(), Query{Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "One" || second != 0 {
		t.Fatalf("results = %+v, brave asked %d times; want only ChatGPT's answer", got, second)
	}
}

// A failed provider is named with a short reason, and no credential appears in
// the message.
func TestChainNamesEveryProviderWhenAllFail(t *testing.T) {
	asked := 0
	chain := Chain{
		fakeProvider{name: "chatgpt", err: errors.New("chatgpt answered HTTP 401 for Bearer sk-secret"), calls: &asked},
		fakeProvider{name: "brave", err: ErrNotSignedIn, calls: &asked},
	}
	_, err := chain.Search(t.Context(), Query{Text: "go"})
	if err == nil {
		t.Fatal("a chain with no provider answered without an error")
	}
	message := err.Error()
	for _, want := range []string{"chatgpt failed: chatgpt answered HTTP 401", "brave is not signed in"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not name %q", message, want)
		}
	}
	if strings.Contains(message, "\n") {
		t.Fatalf("error spans more than one line: %q", message)
	}
}

func TestChainNeedsAQuery(t *testing.T) {
	asked := 0
	_, err := Chain{fakeProvider{name: "brave", calls: &asked}}.Search(t.Context(), Query{Text: "   "})
	if err == nil || asked != 0 {
		t.Fatalf("blank query: err = %v, providers asked %d; want a refusal without a provider call", err, asked)
	}
}

func TestQueryLimitDefaultsAndCaps(t *testing.T) {
	for _, c := range []struct{ limit, want int }{{0, DefaultLimit}, {-3, DefaultLimit}, {2, 2}, {99, MaxLimit}} {
		if got := (Query{Limit: c.limit}).results(); got != c.want {
			t.Errorf("limit %d = %d results, want %d", c.limit, got, c.want)
		}
	}
}
